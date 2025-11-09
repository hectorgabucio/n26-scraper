package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gen2brain/go-fitz"
)

// Transaction represents a parsed transaction from the PDF
type Transaction struct {
	BookingDate string
	ValueDate   string
	PartnerName string
	Amount      string
}

// AccountBalance represents the account balance extracted from the PDF
type AccountBalance struct {
	Balance string // The balance amount (e.g., "1,234.56" or "1.234,56")
}

// PDFParser handles parsing of N26 PDF statements
type PDFParser struct {
	doc *fitz.Document
}

// NewPDFParser creates a new PDF parser from a file path
func NewPDFParser(filePath string) (*PDFParser, error) {
	doc, err := fitz.New(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open PDF: %w", err)
	}

	return &PDFParser{doc: doc}, nil
}

// NewPDFParserFromBytes creates a new PDF parser from byte data
func NewPDFParserFromBytes(data []byte) (*PDFParser, error) {
	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		return nil, fmt.Errorf("failed to open PDF from memory: %w", err)
	}

	return &PDFParser{doc: doc}, nil
}

// Close closes the PDF document
func (p *PDFParser) Close() error {
	if p.doc != nil {
		p.doc.Close()
	}
	return nil
}

// ExtractText extracts all text from the PDF
func (p *PDFParser) ExtractText() (string, error) {
	var textBuilder strings.Builder

	for i := 0; i < p.doc.NumPage(); i++ {
		text, err := p.doc.Text(i)
		if err != nil {
			return "", fmt.Errorf("failed to extract text from page %d: %w", i+1, err)
		}
		textBuilder.WriteString(text)
		if i < p.doc.NumPage()-1 {
			textBuilder.WriteString("\n")
		}
	}

	return textBuilder.String(), nil
}

// ParseTransactions parses transactions from the PDF text
func (p *PDFParser) ParseTransactions() ([]Transaction, error) {
	text, err := p.ExtractText()
	if err != nil {
		return nil, err
	}

	return parseTransactionsFromText(text)
}

// ParseBalance extracts the account balance from the PDF text
func (p *PDFParser) ParseBalance() (*AccountBalance, error) {
	text, err := p.ExtractText()
	if err != nil {
		return nil, err
	}

	return parseBalanceFromText(text)
}

// parseBalanceFromText extracts the account balance from PDF text
func parseBalanceFromText(text string) (*AccountBalance, error) {
	lines := strings.Split(text, "\n")

	// Look for the line containing "Tu nuevo saldo" (Spanish) or "Your new balance" (English)
	balancePattern := regexp.MustCompile(`([+-]?\d+[.,]\d{2})\s*€?`)

	for i, line := range lines {
		line = strings.TrimSpace(line)

		// Check for Spanish balance text
		if strings.Contains(line, "Tu nuevo saldo") || strings.Contains(line, "tu nuevo saldo") {
			// The balance is on the next line
			if i+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[i+1])
				// Extract balance amount from the next line
				amounts := balancePattern.FindAllString(nextLine, -1)
				if len(amounts) > 0 {
					// Take the last amount found (most likely the balance)
					balance := strings.TrimSpace(amounts[len(amounts)-1])
					balance = strings.TrimSuffix(balance, "€")
					balance = strings.TrimSpace(balance)
					return &AccountBalance{Balance: balance}, nil
				}
			}
		}

		// Check for English balance text (if PDF is in English)
		if strings.Contains(line, "Your new balance") || strings.Contains(line, "your new balance") {
			// The balance is on the next line
			if i+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[i+1])
				amounts := balancePattern.FindAllString(nextLine, -1)
				if len(amounts) > 0 {
					balance := strings.TrimSpace(amounts[len(amounts)-1])
					balance = strings.TrimSuffix(balance, "€")
					balance = strings.TrimSpace(balance)
					return &AccountBalance{Balance: balance}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("balance not found in PDF")
}

// parseTransactionsFromText extracts transaction data from PDF text
func parseTransactionsFromText(text string) ([]Transaction, error) {
	var transactions []Transaction

	// Split text into lines
	lines := strings.Split(text, "\n")

	// Patterns for N26 PDF format
	// Date format: DD.MM.YYYY
	// Amount format: -XX,XX€ or +XX,XX€ or XX,XX€
	datePattern := regexp.MustCompile(`(\d{2}\.\d{2}\.\d{4})`)
	// Amount with euro sign: -2,50€ or 2,50€
	amountPattern := regexp.MustCompile(`([+-]?\d+[.,]\d{2})\s*€?`)

	// Look for transaction blocks
	// In N26 PDFs, transactions typically appear as:
	// Partner Name
	// Category/Description
	// Fecha de valor DD.MM.YYYY
	// DD.MM.YYYY
	// -XX,XX€

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Look for amount pattern with euro sign (transaction amount line)
		// Pattern: -XX,XX€ or +XX,XX€ (must have comma as decimal separator for N26)
		if matched, _ := regexp.MatchString(`^[+-]?\d+,\d{2}\s*€?\s*$`, line); matched {
			// Found an amount line, extract it
			amount := strings.TrimSpace(line)
			amount = strings.TrimSuffix(amount, "€")
			amount = strings.TrimSpace(amount)

			// Look backwards for transaction details (up to 10 lines back)
			tx := &Transaction{Amount: amount}

			// Look for dates in previous lines
			for j := max(0, i-10); j < i; j++ {
				checkLine := strings.TrimSpace(lines[j])

				// Check for "Fecha de valor" line
				if strings.Contains(checkLine, "Fecha de valor") {
					dates := datePattern.FindAllString(checkLine, -1)
					if len(dates) > 0 {
						tx.ValueDate = dates[0]
					}
				}

				// Check for booking date (standalone date line, not in "Fecha de valor")
				if tx.BookingDate == "" && !strings.Contains(checkLine, "Fecha de valor") {
					dates := datePattern.FindAllString(checkLine, -1)
					if len(dates) > 0 && len(checkLine) < 20 {
						// Likely a standalone date line
						tx.BookingDate = dates[0]
						if tx.ValueDate == "" {
							tx.ValueDate = dates[0]
						}
					}
				}
			}

			// Look for partner name (usually 3-8 lines before amount)
			// Search from furthest back to closest, to get the first/primary partner name
			for j := max(0, i-8); j < i-1; j++ {
				checkLine := strings.TrimSpace(lines[j])

				// Skip if it's a header, date, amount, or metadata line
				if len(checkLine) < 3 ||
					datePattern.MatchString(checkLine) ||
					amountPattern.MatchString(checkLine) ||
					strings.Contains(checkLine, "Fecha") ||
					strings.Contains(checkLine, "Descripción") ||
					strings.Contains(checkLine, "Cantidad") ||
					strings.Contains(checkLine, "Mastercard") ||
					strings.Contains(checkLine, "IBAN") ||
					strings.Contains(checkLine, "BIC") ||
					strings.Contains(checkLine, "Emitido") ||
					strings.Contains(checkLine, "Transferencias") ||
					strings.Contains(checkLine, "Enviada") ||
					strings.Contains(checkLine, "Actividad") ||
					strings.Contains(checkLine, "salientes") {
					continue
				}

				// This might be the partner name (usually the first substantial line before dates)
				// Prefer longer lines as they're more likely to be partner names
				if len(checkLine) > 2 {
					if tx.PartnerName == "" || len(checkLine) > len(tx.PartnerName) {
						tx.PartnerName = checkLine
					}
				}
			}

			// Only add transaction if it has required fields and valid amount
			if tx.BookingDate != "" && tx.Amount != "" {
				// Validate amount is actually an amount (has comma, not just a date)
				if strings.Contains(tx.Amount, ",") {
					// Filter out non-transaction entries
					if isRealTransaction(tx) {
						if tx.ValueDate == "" {
							tx.ValueDate = tx.BookingDate
						}
						transactions = append(transactions, *tx)
					}
				}
			}
		}
	}

	return transactions, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isRealTransaction checks if a transaction is a real transaction and not a header/balance/page number
func isRealTransaction(tx *Transaction) bool {
	partnerName := strings.TrimSpace(tx.PartnerName)

	// Skip if partner name is empty (might be a balance line)
	if partnerName == "" {
		return false
	}

	// Skip page numbers (e.g., "1 / 3", "2 / 3")
	pageNumberPattern := regexp.MustCompile(`^\d+\s*/\s*\d+$`)
	if pageNumberPattern.MatchString(partnerName) {
		return false
	}

	// Skip balance/header lines
	skipPatterns := []string{
		"Saldo previo",
		"Saldo",
		"Balance",
		"Emitido",
		"Descripción",
		"Fecha de reserva",
		"Cantidad",
		"Actividad de la cuenta",
	}

	partnerLower := strings.ToLower(partnerName)
	for _, pattern := range skipPatterns {
		if strings.Contains(partnerLower, strings.ToLower(pattern)) {
			return false
		}
	}

	// Skip very short partner names (likely not real transactions)
	if len(partnerName) < 3 {
		return false
	}

	return true
}
