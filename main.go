package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"
)

const (
	endpoint = "https://app.n26.com/account-activity/period/$ACCOUNT_ID?endDate=$END_UNIX&format=pdf&startDate=$START_UNIX"
)

// waitForNetworkIdle waits for network activity to settle, similar to Puppeteer's networkidle0/networkidle2.
// maxConnections: 0 for networkidle0, 2 for networkidle2
// idleDuration: how long to wait with no (or few) connections (default 500ms like Puppeteer)
func waitForNetworkIdle(_ context.Context, maxConnections int) chromedp.Action {

	idleDuration := 500 * time.Millisecond

	return chromedp.ActionFunc(func(ctx context.Context) error {
		// Enable network domain
		if err := network.Enable().Do(ctx); err != nil {
			return err
		}

		var mu sync.Mutex
		activeRequests := make(map[string]bool)
		idleSince := time.Now()
		checkInterval := 50 * time.Millisecond
		maxWait := 30 * time.Second

		// Listen to network events
		chromedp.ListenTarget(ctx, func(ev interface{}) {
			mu.Lock()
			defer mu.Unlock()

			switch ev := ev.(type) {
			case *network.EventRequestWillBeSent:
				activeRequests[ev.RequestID.String()] = true
			case *network.EventLoadingFinished:
				delete(activeRequests, ev.RequestID.String())
			case *network.EventLoadingFailed:
				delete(activeRequests, ev.RequestID.String())
			}
		})

		startTime := time.Now()
		for {
			mu.Lock()
			activeCount := len(activeRequests)
			mu.Unlock()

			if activeCount <= maxConnections {
				// Check if we've been idle long enough
				if time.Since(idleSince) >= idleDuration {
					return nil
				}
			} else {
				// Reset idle timer if we have too many connections
				idleSince = time.Now()
			}

			// Check for timeout
			if time.Since(startTime) > maxWait {
				return fmt.Errorf("timeout waiting for network idle (maxConnections: %d, active: %d)", maxConnections, activeCount)
			}

			time.Sleep(checkInterval)

			// Check if context is cancelled
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	})
}

// ErrorResponse represents the 401 error response structure
type ErrorResponse struct {
	Status      int    `json:"status"`
	Detail      string `json:"detail"`
	Type        string `json:"type"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
	UserMessage struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	} `json:"userMessage"`
}

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Get credentials
	email := os.Getenv("N26_EMAIL")
	password := os.Getenv("N26_PASSWORD")

	if email == "" || password == "" {
		log.Fatal("N26_EMAIL and N26_PASSWORD must be set in environment variables or .env file")
	}

	// Initialize repositories - PostgreSQL is required
	dbConn := os.Getenv("DB_CONN")
	if dbConn == "" {
		log.Fatal("DB_CONN environment variable is required. Please set it with your PostgreSQL connection string.")
	}

	// Initialize PostgreSQL cookie repository
	cookieRepo, err := NewPostgresCookieRepository(dbConn)
	if err != nil {
		log.Fatalf("Failed to initialize PostgreSQL cookie repository: %v", err)
	}
	defer func() {
		if err := cookieRepo.Close(); err != nil {
			log.Printf("Warning: Failed to close cookie repository: %v", err)
		}
	}()
	fmt.Println("Using PostgreSQL storage for cookies")

	// Initialize PostgreSQL statement repository (reuse the same DB connection)
	statementRepo, err := NewPostgresStatementRepository(cookieRepo.db)
	if err != nil {
		log.Fatalf("Failed to initialize PostgreSQL statement repository: %v", err)
	}
	fmt.Println("Using PostgreSQL storage for statements")

	// Try to read cookie from repository
	cookieHeader, err := cookieRepo.Get()
	if err != nil {
		log.Printf("Could not read cookie from repository: %v", err)
		log.Println("Will perform login to get new cookie...")
	}

	// Try to call endpoint with cookie
	if cookieHeader != "" {
		fmt.Println("Attempting to call endpoint with stored cookie...")
		pdfData, err := callEndpointWithCookie(cookieHeader)
		if err != nil {
			if isUnauthorizedError(err) {
				log.Println("Cookie expired or invalid. Performing login...")
				cookieHeader = ""
			} else {
				log.Fatalf("Failed to call endpoint: %v", err)
			}
		} else {
			fmt.Println("Successfully called endpoint with stored cookie")

			// Send Discord notification
			if err := sendDiscordNotification(pdfData, statementRepo); err != nil {
				log.Printf("Warning: Failed to send Discord notification: %v", err)
			}

			return
		}
	}

	// If we get here, we need to login
	if cookieHeader == "" {
		fmt.Println("Performing login to get fresh cookie...")
		newCookie, err := performLoginAndGetCookie(email, password)
		if err != nil {
			log.Fatalf("Login failed: %v", err)
		}

		// Save cookie to repository

		if err := cookieRepo.Save(newCookie); err != nil {
			log.Printf("Warning: Failed to save cookie to repository: %v", err)
		} else {
			fmt.Println("Cookie saved successfully")
		}

		fmt.Println("Login completed and cookie saved. Exiting.")
	}
}

// callEndpointWithCookie makes a GET request to the endpoint with the cookie header
// Returns the PDF data on success
func callEndpointWithCookie(cookieHeader string) ([]byte, error) {
	endUnix := time.Now().Unix() * 1000
	startUnix := time.Now().AddDate(0, 0, -30).Unix() * 1000

	endpointWithUnix := strings.Replace(endpoint, "$END_UNIX", fmt.Sprintf("%d", endUnix), 1)
	endpointWithUnix = strings.Replace(endpointWithUnix, "$START_UNIX", fmt.Sprintf("%d", startUnix), 1)
	endpointWithAccountId := strings.Replace(endpointWithUnix, "$ACCOUNT_ID", os.Getenv("N26_ACCOUNT_ID"), 1)
	req, err := http.NewRequest("GET", endpointWithAccountId, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body first
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check if we got a 401 error in HTTP status
	if resp.StatusCode == http.StatusUnauthorized {
		// Return error with body so isUnauthorizedError can parse the JSON
		return nil, fmt.Errorf("401 unauthorized: %s", string(body))
	}

	// Check if response body contains a JSON error (even if HTTP status is 200)
	// Sometimes the API returns 200 OK but with a JSON error in the body
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') {
		var errResp ErrorResponse
		if json.Unmarshal(body, &errResp) == nil {
			// If we successfully parsed JSON and it's an error response
			if errResp.Status == 401 || errResp.Error == "invalid_token" {
				return nil, fmt.Errorf("401 unauthorized: %s", string(body))
			}
			// Check for other error statuses in JSON
			if errResp.Status >= 400 {
				return nil, fmt.Errorf("request failed with status %d: %s", errResp.Status, string(body))
			}
		}
	}

	// Check for other HTTP errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Success - we have PDF data
	fmt.Printf("Successfully retrieved PDF data (%d bytes)\n", len(body))
	return body, nil
}

// DiscordWebhookPayload represents the JSON structure for Discord webhook
type DiscordWebhookPayload struct {
	Content string `json:"content,omitempty"`
	Embeds  []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Color       int    `json:"color"` // 0x00FF00 for green (success)
		Fields      []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Inline bool   `json:"inline,omitempty"`
		} `json:"fields,omitempty"`
		Timestamp string `json:"timestamp,omitempty"`
	} `json:"embeds,omitempty"`
}

// sendDiscordNotification sends a notification to Discord webhook when PDF is successfully downloaded
// Only notifies about statements that haven't been notified before
func sendDiscordNotification(pdfData []byte, statementRepo StatementRepository) error {
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		return fmt.Errorf("WEBHOOK_URL environment variable is not set")
	}

	// Parse PDF data
	parser, err := NewPDFParserFromBytes(pdfData)
	if err != nil {
		return fmt.Errorf("failed to create PDF parser: %w", err)
	}
	defer parser.Close()

	// Extract and log small pa
	extractedText, err := parser.ExtractText()
	if err != nil {
		return fmt.Errorf("failed to extract PDF text: %w", err)
	}
	detectedLanguage := "en"
	if strings.Contains(extractedText, "Actividad de la cuenta") {
		detectedLanguage = "es"
	}
	log.Printf("Extracted PDF text (%d characters)\n", len(extractedText))
	log.Printf("Detected language: %s", detectedLanguage)

	transactions, err := parser.ParseTransactions()
	if err != nil {
		return fmt.Errorf("failed to parse PDF transactions: %w", err)
	}

	if len(transactions) == 0 {
		return fmt.Errorf("PDF has no transaction data")
	}

	// Parse account balance
	var accountBalance string
	balance, err := parser.ParseBalance()
	if err != nil {
		log.Printf("Warning: Failed to parse account balance: %v", err)
		accountBalance = "N/A"
	} else {
		accountBalance = balance.Balance
		log.Printf("Account balance: %s EUR", accountBalance)
	}

	// Collect all statements and filter out already notified ones
	type Statement struct {
		Date    string
		Partner string
		Amount  string
		Key     string
	}

	var newStatements []Statement

	for _, tx := range transactions {
		// Convert Transaction to Statement format
		bookingDate := tx.BookingDate
		partnerName := tx.PartnerName
		amount := tx.Amount

		key := generateStatementKey(bookingDate, partnerName, amount)

		// Check if already notified
		notified, err := statementRepo.IsNotified(key)
		if err != nil {
			log.Printf("Warning: Failed to check if statement is notified: %v", err)
			// Assume not notified if we can't check
			notified = false
		}

		if !notified {
			newStatements = append(newStatements, Statement{
				Date:    bookingDate,
				Partner: partnerName,
				Amount:  amount,
				Key:     key,
			})
		}
	}

	// If no new statements, skip notification
	if len(newStatements) == 0 {
		fmt.Println("No new statements to notify. All statements have already been notified.")
		return nil
	}

	fmt.Printf("Found %d new statements out of %d total statements\n", len(newStatements), len(transactions))

	// Format transactions (limit to first 10 for Discord embed)
	var transactionsText strings.Builder
	maxTransactions := 10
	if len(newStatements) < maxTransactions {
		maxTransactions = len(newStatements)
	}

	for i := 0; i < maxTransactions; i++ {
		stmt := newStatements[i]
		// Format: Date | Partner Name | Amount
		transactionsText.WriteString(fmt.Sprintf("**%s** | %s | `%s EUR`\n", stmt.Date, stmt.Partner, stmt.Amount))
	}

	if len(newStatements) > maxTransactions {
		transactionsText.WriteString(fmt.Sprintf("\n_... and %d more new transactions_", len(newStatements)-maxTransactions))
	}

	// Create Discord embed
	fields := []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline,omitempty"`
	}{
		{
			Name:   "New Transactions",
			Value:  fmt.Sprintf("%d", len(newStatements)),
			Inline: true,
		},
		{
			Name:   "Total Transactions",
			Value:  fmt.Sprintf("%d", len(transactions)),
			Inline: true,
		},
		{
			Name:   "Account Balance",
			Value:  fmt.Sprintf("%s EUR", accountBalance),
			Inline: true,
		},
	}

	fields = append(fields, struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline,omitempty"`
	}{
		Name:   "Transactions",
		Value:  transactionsText.String(),
		Inline: false,
	})

	// Create content message for notification preview with all new transactions
	var contentBuilder strings.Builder
	for _, stmt := range newStatements {
		contentBuilder.WriteString(fmt.Sprintf("**%s** | %s | `%s EUR`\n\n", stmt.Date, stmt.Partner, stmt.Amount))
	}

	contentMsg := strings.TrimSpace(contentBuilder.String())

	payload := DiscordWebhookPayload{
		Content: contentMsg,
		Embeds: []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			Fields      []struct {
				Name   string `json:"name"`
				Value  string `json:"value"`
				Inline bool   `json:"inline,omitempty"`
			} `json:"fields,omitempty"`
			Timestamp string `json:"timestamp,omitempty"`
		}{
			{
				Title:       "✅ N26 PDF Movements",
				Description: "",
				Color:       0x00FF00, // Green color
				Fields:      fields,
				Timestamp:   time.Now().Format(time.RFC3339),
			},
		},
	}

	// Marshal JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal Discord payload: %w", err)
	}

	// Send HTTP POST request
	req, err := http.NewRequest("POST", webhookURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to create Discord webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord webhook returned status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Println("Discord notification sent successfully!")

	// Mark all statements as notified after successful webhook
	var notifiedKeys []string
	for _, stmt := range newStatements {
		notifiedKeys = append(notifiedKeys, stmt.Key)
	}

	if err := statementRepo.MarkMultipleAsNotified(notifiedKeys); err != nil {
		log.Printf("Warning: Failed to mark statements as notified: %v", err)
		// Don't return error, notification was sent successfully
	} else {
		fmt.Printf("Marked %d statements as notified\n", len(notifiedKeys))
	}

	return nil
}

// isUnauthorizedError checks if the error is a 401 unauthorized error
func isUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// Check for 401 in error message
	if strings.Contains(errStr, "401") || strings.Contains(strings.ToLower(errStr), "unauthorized") {
		// Try to parse as JSON error response to confirm
		if strings.Contains(errStr, "{") {
			jsonStart := strings.Index(errStr, "{")
			if jsonStart >= 0 {
				var errResp ErrorResponse
				if json.Unmarshal([]byte(errStr[jsonStart:]), &errResp) == nil {
					return errResp.Status == 401 || errResp.Error == "invalid_token"
				}
			}
		}
		// If it contains 401 or unauthorized, assume it's a 401 error
		return true
	}

	return false
}

// performLoginAndGetCookie performs login with 2FA and extracts the cookie
func performLoginAndGetCookie(email, password string) (string, error) {
	// Setup Chrome context (headless)
	ctx, cancel := setupChromeContext()
	defer cancel()

	// Login to N26
	if err := loginToN26(ctx, email, password); err != nil {
		return "", fmt.Errorf("login failed: %w", err)
	}

	// Wait a bit for session to be established
	time.Sleep(2 * time.Second)

	// Extract cookies
	cookies, err := extractCookies(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to extract cookies: %w", err)
	}

	// Convert cookies to Cookie header format
	cookieHeader := formatCookiesAsHeader(cookies)
	if cookieHeader == "" {
		return "", fmt.Errorf("no cookies found after login")
	}

	return cookieHeader, nil
}

// setupChromeContext creates and configures the Chrome context (headless)
func setupChromeContext() (context.Context, context.CancelFunc) {
	ctx, _ := chromedp.NewExecAllocator(
		context.Background(),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("enable-features", "NetworkService,NetworkServiceLogging"),
		chromedp.Flag("disable-features", "TranslateUI"),
		//chromedp.Flag("lang", "es-ES"),                 // Set browser language to Spanish (Spain)
		//chromedp.Flag("accept-lang", "es-ES,es;q=0.9"), // Set Accept-Language header to Spanish
		chromedp.UserDataDir(filepath.Join(os.TempDir(), "chromedp-n26-cookie")),
		chromedp.ExecPath(""),
	)

	ctx, _ = chromedp.NewContext(ctx)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	return ctx, cancel
}

// loginToN26 handles the login process including 2FA
func loginToN26(ctx context.Context, email, password string) error {
	fmt.Println("Opening N26 website...")
	var currentURL string
	err := chromedp.Run(ctx,
		chromedp.Navigate("https://app.n26.com/login"),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		// Set the free trial cookie after page loads
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Set the cookie to prevent free trial popup
			// The value %5B%22SMART%22%5D is URL-encoded ["SMART"]
			err := network.SetCookie("n26.free_trial_opt_in_seen", "%5B%22SMART%22%5D").WithDomain("app.n26.com").
				WithPath("/").
				WithHTTPOnly(false).
				WithSecure(true).
				WithSameSite(network.CookieSameSiteLax).
				Do(ctx)

			if err != nil {
				log.Printf("Warning: Failed to set free trial cookie: %v", err)
				// Continue anyway, as this is not critical
			} else {
				fmt.Println("Free trial cookie set successfully")
			}
			return nil
		}),
		waitForNetworkIdle(ctx, 0),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		return fmt.Errorf("failed to navigate to N26: %w", err)
	}

	fmt.Printf("Current URL: %s\n", currentURL)

	// Check if already logged in
	isLoggedIn := strings.Contains(currentURL, "feed") || (strings.Contains(currentURL, "app.n26.com") && !strings.Contains(currentURL, "login"))

	if !isLoggedIn {
		if err := fillLoginForm(ctx, email, password); err != nil {
			return err
		}
		if err := submitLoginForm(ctx); err != nil {
			return err
		}
	} else {
		fmt.Println("Already logged in! Skipping login form...")
	}

	// Wait for login to complete
	if err := waitForLoginCompletion(ctx, &currentURL); err != nil {
		return err
	}

	fmt.Printf("Current URL after login: %s\n", currentURL)

	// Handle 2FA if needed
	if err := handle2FA(ctx, currentURL); err != nil {
		return err
	}

	return nil
}

// fillLoginForm fills in the email and password fields
func fillLoginForm(ctx context.Context, email, password string) error {
	fmt.Println("Filling login form...")
	err := chromedp.Run(ctx,
		chromedp.WaitVisible("input[type='email'], input[name='email'], input[id='email'], input[placeholder*='email' i]", chromedp.ByQuery),
		chromedp.SendKeys("input[type='email'], input[name='email'], input[id='email'], input[placeholder*='email' i]", email, chromedp.ByQuery),
		chromedp.WaitVisible("input[type='password'], input[name='password'], input[id='password']", chromedp.ByQuery),
	)
	if err != nil {
		return fmt.Errorf("failed to fill email: %w", err)
	}

	err = chromedp.Run(ctx,
		chromedp.SendKeys("input[type='password'], input[name='password'], input[id='password']", password, chromedp.ByQuery),
	)
	if err != nil {
		return fmt.Errorf("failed to fill password: %w", err)
	}

	return nil
}

// submitLoginForm submits the login form
func submitLoginForm(ctx context.Context) error {
	fmt.Println("Submitting login form...")
	time.Sleep(3 * time.Second)
	err := chromedp.Run(ctx,
		chromedp.KeyEvent("\n"),
		waitForNetworkIdle(ctx, 0),
	)
	if err != nil {
		return fmt.Errorf("failed to submit login: %w", err)
	}
	return nil
}

// waitForLoginCompletion waits for the login process to complete
func waitForLoginCompletion(ctx context.Context, currentURL *string) error {
	fmt.Println("Waiting for login to complete...")
	time.Sleep(3 * time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible("body", chromedp.ByQuery),
		waitForNetworkIdle(ctx, 0),
		chromedp.Location(currentURL),
	)
	if err != nil {
		return fmt.Errorf("failed to get current URL: %w", err)
	}
	time.Sleep(3 * time.Second)
	return nil
}

// handle2FA handles the 2FA step if required
func handle2FA(ctx context.Context, currentURL string) error {
	// If URL contains /feed, we are already logged in, skip 2FA step
	if strings.Contains(currentURL, "/feed") {
		fmt.Println("Already logged in (on feed page), skipping 2FA step")
		return nil
	}

	// Check if we're in 2FA step
	var h1Text string
	err := chromedp.Run(ctx,
		chromedp.Text("h1", &h1Text, chromedp.ByQuery),
	)

	if err != nil {
		log.Printf("Failed to get h1 text: %v", err)
	}

	confirms := []string{
		"Confirm your login",
		"Confirma el inicio de",
	}

	fmt.Println("h1Text: ", h1Text)

	if err == nil && slices.Contains(confirms, h1Text) {
		return waitFor2FAConfirmation(ctx)
	}

	// No 2FA step, check if login was successful
	if strings.Contains(currentURL, "/login") {
		return fmt.Errorf("login failed")
	}

	fmt.Println("Login successful (no 2FA required)")
	return nil
}

// waitFor2FAConfirmation waits for the user to confirm 2FA on their phone
func waitFor2FAConfirmation(ctx context.Context) error {
	fmt.Println("2FA step detected - waiting for you to confirm on your phone...")
	fmt.Println("Waiting for 2FA confirmation to complete...")

	ctx2FA, cancel2FA := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2FA()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-ctx2FA.Done():
			return fmt.Errorf("2FA confirmation timeout: no confirmation received within 60 seconds")

		case <-ticker.C:
			var currentURL string
			err := chromedp.Run(ctx2FA,
				chromedp.Location(&currentURL),
			)

			if err != nil {
				log.Printf("Error checking URL: %v", err)
				continue
			}

			// If URL doesn't contain /login, 2FA is confirmed
			if !strings.Contains(currentURL, "/login") {
				fmt.Println("2FA confirmed! Successfully logged in.")
				return nil
			}

			// Still on /login, continue waiting
			elapsed := time.Since(startTime)
			fmt.Printf("Still waiting for 2FA confirmation... (elapsed: %v)\n", elapsed.Round(time.Second))
		}
	}
}

// extractCookies extracts all cookies from the Chrome session
func extractCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			cookiesResult, err := network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			cookies = cookiesResult
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}
	return cookies, nil
}

// formatCookiesAsHeader formats cookies as a Cookie header string
func formatCookiesAsHeader(cookies []*network.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}

	var parts []string
	for _, cookie := range cookies {
		// Only include cookies for app.n26.com domain
		if strings.Contains(cookie.Domain, "n26.com") {
			parts = append(parts, fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
		}
	}

	return strings.Join(parts, "; ")
}
