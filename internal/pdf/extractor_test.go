package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-api/internal/pdf"
)

// buildMinimalPDF returns a valid binary PDF with the given text embedded in a content stream.
func buildMinimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT\n/F1 12 Tf\n100 700 Td\n(%s) Tj\nET", text)
	body := fmt.Sprintf("%%PDF-1.4\n"+
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"+
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n"+
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n"+
		"4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n"+
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
		len(stream), stream)

	// Offsets for xref
	off1 := 9
	off2 := off1 + len("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off3 := off2 + len("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off4 := off3 + len("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	off5 := off4 + len(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	startXref := len(body)

	xrefAndTrailer := fmt.Sprintf("xref\n0 6\n0000000000 65535 f \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF",
		off1, off2, off3, off4, off5, startXref)

	return []byte(body + xrefAndTrailer)
}

func TestPDFExtractor_Table(t *testing.T) {
	extractor := pdf.NewExtractor()

	t.Run("Empty payload returns validation error", func(t *testing.T) {
		r := bytes.NewReader([]byte{})
		_, err := extractor.ExtractText(context.Background(), r, 0)
		if err == nil {
			t.Fatal("expected error for empty payload, got nil")
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected ErrValidation, got: %v", err)
		}
	})

	t.Run("Negative size returns validation error", func(t *testing.T) {
		r := bytes.NewReader([]byte("%PDF-1.4"))
		_, err := extractor.ExtractText(context.Background(), r, -1)
		if err == nil {
			t.Fatal("expected error for negative size, got nil")
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected ErrValidation, got: %v", err)
		}
	})

	t.Run("Non-PDF header returns validation error", func(t *testing.T) {
		data := []byte("<html><body>Not a PDF</body></html>")
		r := bytes.NewReader(data)
		_, err := extractor.ExtractText(context.Background(), r, int64(len(data)))
		if err == nil {
			t.Fatal("expected error for non-PDF content, got nil")
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected ErrValidation, got: %v", err)
		}
	})

	t.Run("Corrupted PDF returns validation error", func(t *testing.T) {
		data := []byte("%PDF-1.4\nCorrupted garbage data that is not a valid PDF")
		r := bytes.NewReader(data)
		_, err := extractor.ExtractText(context.Background(), r, int64(len(data)))
		if err == nil {
			t.Fatal("expected error for corrupted PDF, got nil")
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected ErrValidation, got: %v", err)
		}
	})

	t.Run("Valid PDF with text extracts successfully", func(t *testing.T) {
		expectedText := "Jane Doe Senior Engineer"
		pdfBytes := buildMinimalPDF(expectedText)
		r := bytes.NewReader(pdfBytes)
		extracted, err := extractor.ExtractText(context.Background(), r, int64(len(pdfBytes)))
		if err != nil {
			t.Fatalf("unexpected error extracting text: %v", err)
		}
		if !bytes.Contains([]byte(extracted), []byte("Jane Doe")) {
			t.Fatalf("extracted text does not contain 'Jane Doe', got: %q", extracted)
		}
	})

	t.Run("Blank PDF with no text returns validation error", func(t *testing.T) {
		blankPDF := buildMinimalPDF("")
		r := bytes.NewReader(blankPDF)
		_, err := extractor.ExtractText(context.Background(), r, int64(len(blankPDF)))
		if err == nil {
			t.Fatal("expected error for blank PDF, got nil")
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected ErrValidation, got: %v", err)
		}
	})

	t.Run("Context cancelled returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pdfBytes := buildMinimalPDF("Sample Content")
		r := bytes.NewReader(pdfBytes)
		_, err := extractor.ExtractText(ctx, r, int64(len(pdfBytes)))
		if err == nil {
			t.Fatal("expected error with cancelled context, got nil")
		}
	})
}
