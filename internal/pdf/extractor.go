package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"

	"resumeee/internal/domain"
)

// PDFTextExtractor abstracts extracting textual content from PDF binary streams.
type PDFTextExtractor interface {
	ExtractText(ctx context.Context, r io.ReaderAt, size int64) (string, error)
}

type defaultPDFExtractor struct{}

// NewExtractor constructs a pure-Go PDF text extractor based on ledongthuc/pdf.
func NewExtractor() PDFTextExtractor {
	return &defaultPDFExtractor{}
}

// ExtractText validates PDF headers, extracts plain text content from all pages,
// and enforces domain validation boundaries.
func (e *defaultPDFExtractor) ExtractText(ctx context.Context, r io.ReaderAt, size int64) (string, error) {
	if r == nil || size <= 0 {
		return "", fmt.Errorf("%w: empty file payload", domain.ErrValidation)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Verify magic bytes header
	header := make([]byte, 5)
	n, err := r.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("%w: unable to read file header: %v", domain.ErrValidation, err)
	}
	if n < 4 || !bytes.HasPrefix(header[:n], []byte("%PDF")) {
		return "", fmt.Errorf("%w: file header does not contain %%PDF-", domain.ErrValidation)
	}

	reader, err := pdf.NewReader(r, size)
	if err != nil {
		return "", fmt.Errorf("%w: invalid or corrupted PDF document: %v", domain.ErrValidation, err)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	plainTextReader, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("%w: failed to read plain text from PDF: %v", domain.ErrValidation, err)
	}

	rawBytes, err := io.ReadAll(plainTextReader)
	if err != nil {
		return "", fmt.Errorf("%w: failed to read plain text stream: %v", domain.ErrValidation, err)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	text := strings.TrimSpace(string(rawBytes))
	if text == "" {
		return "", fmt.Errorf("%w: PDF contains no extractable text", domain.ErrValidation)
	}

	return text, nil
}
