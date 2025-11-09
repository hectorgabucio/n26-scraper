# N26 PDF Scraper

A Go program that automates logging into your N26 account, downloading PDF transaction statements, parsing them, and sending notifications via Discord webhook. Uses PostgreSQL for persistent storage of cookies and statement tracking.

## Features

- 🔐 **Automated Login**: Handles N26 login with 2FA support (English and Spanish)
- 📄 **PDF Download & Parsing**: Automatically downloads and parses PDF transaction statements
- 🔔 **Discord Notifications**: Sends formatted transaction notifications with account balance
- 🗄️ **PostgreSQL Storage**: Persistent storage for cookies and statement tracking
- 🚫 **Duplicate Prevention**: Tracks notified statements to avoid duplicates
- 🌍 **Multi-language Support**: Supports both English and Spanish PDF formats
- ⚙️ **Manual Execution**: GitHub Actions workflow for on-demand execution

## Requirements

- Go 1.25 or later
- Chrome or Chromium browser installed on your system
- PostgreSQL database (local or cloud-hosted like Neon, Supabase, etc.)
- N26 account credentials
- Discord webhook URL (optional, for notifications)

## Setup

1. **Clone this repository:**
   ```bash
   git clone <your-repo-url>
   cd n26-scraper
   ```

2. **Install dependencies:**
   ```bash
   go mod download
   ```

3. **Set up PostgreSQL database:**
   - Create a PostgreSQL database (local or cloud-hosted)
   - Get your connection string (format: `postgresql://user:password@host:port/database?sslmode=require`)
   - The application will automatically run migrations to create required tables

4. **Configure environment variables:**
   
   Create a `.env` file or set environment variables:
   ```bash
   N26_EMAIL=your-email@example.com
   N26_PASSWORD=your-password
   N26_ACCOUNT_ID=your-account-id
   DB_CONN=postgresql://user:password@host:port/database?sslmode=require
   WEBHOOK_URL=https://discord.com/api/webhooks/your-webhook-url
   ```

   **Required variables:**
   - `N26_EMAIL`: Your N26 email address
   - `N26_PASSWORD`: Your N26 password
   - `N26_ACCOUNT_ID`: Your N26 account ID (found in the URL: `app.n26.com/spaces/details/{ACCOUNT_ID}`)
   - `DB_CONN`: PostgreSQL connection string

   **Optional variables:**
   - `WEBHOOK_URL`: Discord webhook URL for notifications (if not set, PDF will be downloaded but no notification sent)

## Usage

Run the program:
```bash
go run .
```

Or build and run:
```bash
go build -o n26-scraper .
./n26-scraper
```

The program will:
1. Connect to PostgreSQL and run database migrations
2. Check for existing authentication cookie in the database
3. If no valid cookie exists, perform login (with 2FA if required)
4. Download PDF transaction statement for the last 30 days
5. Parse transactions and account balance from the PDF
6. Filter out already-notified statements
7. Send Discord notification with new transactions and account balance (if webhook is configured)
8. Store cookie and mark statements as notified in PostgreSQL

## How it Works

### Architecture

- **Repository Pattern**: Separated storage logic into repositories
  - `cookie_repository.go`: Handles cookie storage/retrieval
  - `statement_repository.go`: Tracks which statements have been notified
- **PDF Parser**: Custom parser for extracting transactions and balance from N26 PDF statements
  - Supports both English and Spanish PDF formats
  - Extracts: Booking Date, Value Date, Partner Name, Amount, and Account Balance
- **Database Migrations**: Uses `golang-migrate` for schema management
- **Chrome Automation**: Uses `chromedp` for browser automation with Spanish locale support

### Database Schema

The application automatically creates two tables:

**cookies**:
- Stores authentication cookies with timestamps
- Keeps history of all cookies

**statements**:
- Tracks which statements have been notified
- Prevents duplicate notifications

### Discord Notification Format

When new transactions are found, a Discord notification is sent with:
- **Content**: List of all new transactions (visible in notification preview)
- **Embed**: Detailed information including:
  - New transactions count
  - Total transactions count
  - Account balance (current balance from PDF)
  - Formatted transaction list (Booking Date | Partner Name | Amount)

Example notification:
```
✅ **3 new transaction(s)** from N26

**2025-10-10** | ONE| `-2.5 EUR`
**2025-10-10** | TWO | `-1.45 EUR`
**2025-11-02** | THREE | `-9.02 EUR`
```

The embed also includes the current account balance extracted from the PDF.

## GitHub Actions Setup

This project includes a GitHub Actions workflow that can be triggered manually on-demand.

### Setup Instructions

1. **Push the code to a GitHub repository**
   - The repository can be public or private
   - Public repos get unlimited free minutes
   - Private repos get 2,000 free minutes/month

2. **Configure GitHub Secrets**:
   - Go to your repository → Settings → Secrets and variables → Actions
   - Add the following secrets:
     - `N26_EMAIL`: Your N26 email address
     - `N26_PASSWORD`: Your N26 password
     - `N26_ACCOUNT_ID`: Your N26 account ID
     - `DB_CONN`: Your PostgreSQL connection string
     - `WEBHOOK_URL`: Your Discord webhook URL

3. **Enable scheduled runs** (optional):
   - Edit `.github/workflows/n26-scraper.yml`
   - Uncomment the schedule section:
     ```yaml
     on:
       schedule:
         - cron: '0 */2 * * *'  # Every 2 hours
       workflow_dispatch:
     ```
   - Cron format: `minute hour day month day-of-week` (UTC time)
   - Examples:
     - `'0 */2 * * *'` - Every 2 hours
     - `'0 2 * * *'` - Daily at 2:00 AM UTC
     - `'0 */6 * * *'` - Every 6 hours

4. **Manual trigger**:
   - Go to Actions tab → N26 Scraper → Run workflow
   - Click "Run workflow" button

### How GitHub Actions Works

- **Database Storage**: All data (cookies, statements) is stored in PostgreSQL
- **Automatic Migrations**: Database migrations run automatically on each execution
- **Cookie Persistence**: Cookies are stored in PostgreSQL, so login is only needed when cookie expires
- **Duplicate Prevention**: Statement tracking ensures you only get notified about new transactions
- **2FA Support**: The workflow handles 2FA automatically (you may need to monitor the first run)

## Project Structure

```
n26-scraper/
├── main.go                    # Main application logic
├── cookie_repository.go        # Cookie storage repository
├── statement_repository.go     # Statement tracking repository
├── pdf_parser.go              # PDF parsing logic
├── migrations.go                # Database migration runner
├── migrations/                 # SQL migration files
│   ├── 000001_create_cookies_table.up.sql
│   ├── 000001_create_cookies_table.down.sql
│   ├── 000002_create_statements_table.up.sql
│   └── 000002_create_statements_table.down.sql
├── .github/workflows/          # GitHub Actions workflow
└── README.md
```

## Troubleshooting

- **Database connection fails**: Verify your `DB_CONN` connection string is correct
- **Login fails**: Check your N26 credentials in environment variables
- **2FA timeout**: The workflow waits 60 seconds for 2FA confirmation. Monitor the first run.
- **No notifications**: Check that `WEBHOOK_URL` is set and the Discord webhook is valid
- **Duplicate notifications**: Ensure the database is accessible and migrations have run successfully
- **PDF parsing fails**: The parser supports both English and Spanish PDFs. If parsing fails, check the extracted text in logs.
- **Balance not found**: The balance parser looks for "Tu nuevo saldo" (Spanish) or "Your new balance" (English) in the PDF

## Security Notes

### Local Development
- Never commit your `.env` file to version control (already in `.gitignore`)
- Never commit database credentials
- Use environment variables or secure secret management

### GitHub Actions
- **Always use GitHub Secrets** for sensitive data
- **Use a PRIVATE repository** to protect your database connection string
- Limit repository collaborators to trusted individuals only
- Regularly rotate your N26 password
- Use a dedicated PostgreSQL database with restricted access if possible

### Database Security
- Use SSL/TLS connections (`sslmode=require` in connection string)
- Restrict database access to only necessary IPs
- Use strong database passwords
- Regularly backup your database

## License

MIT License

Copyright (c) 2025

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
