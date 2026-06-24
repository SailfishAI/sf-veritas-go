package sfveritas

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
)

var (
	originalStdout *os.File
	printCaptureWg sync.WaitGroup
	inPrintCapture atomic.Bool
)

// startPrintCapture redirects os.Stdout through a pipe so that all
// fmt.Print / fmt.Println / os.Stdout.Write output is captured and
// sent as CollectPrintStatements mutations, while still being written
// to the original stdout.
func startPrintCapture() {
	originalStdout = os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		if cfg := getConfig(); cfg != nil && cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Failed to create stdout pipe: %v\n", err)
		}
		return
	}

	os.Stdout = w

	printCaptureWg.Add(1)
	go func() {
		defer printCaptureWg.Done()
		scanner := bufio.NewScanner(r)
		// Increase scanner buffer for large prints
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			// Always write to original stdout
			fmt.Fprintln(originalStdout, line)

			// Reentrancy guard
			if !inPrintCapture.CompareAndSwap(false, true) {
				continue
			}

			cfg := getConfig()
			if cfg != nil {
				// Best-effort source file/line (may not resolve for fmt.Print)
				_, file, lineNo, ok := runtime.Caller(0)
				sourceFile := ""
				sourceLine := 0
				if ok {
					sourceFile = file
					sourceLine = lineNo
				}

				sessionID := sessionIDFromContext(context.Background())

				vars := mergeVariables(map[string]interface{}{
					"sessionId":                sessionID,
					"contents":                 line,
					"reentrancyGuardPreactive": false,
					"library":                  LibraryType,
					"version":                  Version,
					"parentSpanId":             nil,
					"sourceFile":               nilIfEmpty(sourceFile),
					"sourceLine":               nilIntIfZero(sourceLine),
				})

				nonBlockingPost("CollectPrintStatements", mutationCollectPrintStatements, vars)

				if cfg.debug {
					fmt.Fprintf(os.Stderr, "[sfveritas] Captured print: %q\n", line)
				}
			}

			inPrintCapture.Store(false)
		}
	}()
}

// stopPrintCapture restores the original stdout and waits for the capture
// goroutine to finish processing.
func stopPrintCapture() {
	if originalStdout == nil {
		return
	}
	// Close the write end of the pipe to signal the scanner goroutine
	os.Stdout.Close()
	os.Stdout = originalStdout
	printCaptureWg.Wait()
}

// TransmitPrint sends a print statement to the Sailfish backend manually.
func TransmitPrint(ctx context.Context, message string) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	_, file, line, _ := runtime.Caller(1)
	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"contents":                 message,
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"parentSpanId":             nilIfEmpty(parentSpanID),
		"sourceFile":               nilIfEmpty(file),
		"sourceLine":               nilIntIfZero(line),
	})

	nonBlockingPost("CollectPrintStatements", mutationCollectPrintStatements, vars)
}
