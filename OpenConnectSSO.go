package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"

	"github.com/PhilippePitzClairoux/openconnect-sso/internal"
	"github.com/chromedp/chromedp"
)

// flags
var server = flag.String("server", "", "Server to connect to via openconnect")
var username = flag.String("username", "", "Username to inject in login form")
var password = flag.String("password", "", "Password to inject in login form")
var extraArgs = flag.String("extra-args", "", "Extra args for openconnect (will not override pre-existing ones)")

func main() {
	flag.Parse()

	// Register kill/interrupt signals
	exit := make(chan os.Signal)
	signal.Notify(exit, os.Kill, os.Interrupt)

	// Initialize http clients and start authentication process
	client := internal.NewHttpClient(*server)
	cookieFound := make(chan string)
	targetUrl := internal.GetActualUrl(client, *server)
	samlAuth := internal.AuthenticationInit(client, targetUrl)
	ctx, closeBrowser := internal.CreateBrowserContext()

	// Here we setup a listener to catch the event of a user closing their browser.
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		internal.CloseBrowserOnRenderProcessGone(ev, exit)
	})

	// generate tasks
	tasks := generateDefaultBrowserTasks(samlAuth)

	// close browser at the end - no matter what happens
	defer closeBrowser()

	// handle exit signal
	go handleExit(exit, closeBrowser)

	log.Println("Starting goroutine that searches for authentication cookie ", samlAuth.Auth.SsoV2TokenCookieName)
	go internal.BrowserCookieFinder(ctx, cookieFound, samlAuth.Auth.SsoV2TokenCookieName)

	log.Println("Open browser and navigate to SSO login page : ", samlAuth.Auth.SsoV2Login)
	err := chromedp.Run(ctx, tasks)
	if err != nil {
		log.Fatal(err)
	}

	// consume cookie and connect to vpn
	startVpnOnLoginCookie(cookieFound, client, samlAuth, targetUrl, closeBrowser)
}

func handleExit(exit chan os.Signal, browser context.CancelFunc) {
	sig := <-exit
	log.Printf("Got an exit signal (%s)! Cya!", sig.String())
	browser()
	os.Exit(0)
}

func generateDefaultBrowserTasks(samlAuth *internal.AuthenticationInitExpectedResponse) chromedp.Tasks {
	var tasks chromedp.Tasks

	// create list of tasks to be executed by browser
	tasks = append(tasks, chromedp.Navigate(samlAuth.Auth.SsoV2Login))
	addAutofillTaskOnValue(&tasks, *password, "#passwordInput")
	addAutofillTaskOnValue(&tasks, *username, "#userNameInput")

	return tasks
}

func addAutofillTaskOnValue(actions *chromedp.Tasks, value, selector string) {
	if value != "" {
		*actions = append(
			*actions,
			chromedp.WaitVisible(selector, chromedp.ByID),
			chromedp.SendKeys(selector, value, chromedp.ByID),
		)
	}
}

// startVpnOnLoginCookie waits to get a cookie from the authenticationCookies channel before confirming
// the authentication process (to get token/cert) and then starting openconnect
func startVpnOnLoginCookie(authenticationCookies chan string, client *http.Client, auth *internal.AuthenticationInitExpectedResponse, targetUrl string, closeBrowser context.CancelFunc) {
	for cookie := range authenticationCookies {
		token, cert := internal.AuthenticationConfirmation(client, auth, cookie, targetUrl)
		closeBrowser() // close browser

		command := exec.Command("sudo",
			"openconnect",
			fmt.Sprintf("--useragent=AnyConnect Linux_64 %s", internal.VERSION),
			fmt.Sprintf("--version-string=%s", internal.VERSION),
			fmt.Sprintf("--cookie=%s", token),
			fmt.Sprintf("--servercert=%s", cert),
			targetUrl,
		)

		command.Stdout = os.Stdout
		command.Stderr = os.Stdout
		command.Stdin = os.Stdin

		log.Println("Starting openconnect: ", command.String())
		err := command.Run()
		if err != nil {
			log.Fatal("Could not start command : ", err)
		}
	}
}
