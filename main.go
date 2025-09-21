package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type dlResult struct {
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func main() {
	rand.Seed(time.Now().UnixNano())

	var (
		rawURL     string
		startStr   string
		endStr     string
		interval   int
		jitterFrac float64
		retries    int
		timeout    int
		maxWait    int
		backoff    float64
		maxErrors  int
		ext        string
		ua         string
		quiet      bool
	)
	flag.StringVar(&rawURL, "url", "", "Full URL to any page (e.g. .../0001.png or .../0064.png)")
	flag.StringVar(&startStr, "start", "", "Start page as it appears in filename, e.g. 0001 or 0064 (required)")
	flag.StringVar(&endStr, "end", "", "End page (you can type 0077 or 77). Default = start")
	flag.IntVar(&interval, "interval", 6, "Base interval in seconds between files")
	flag.Float64Var(&jitterFrac, "jitter", 0.2, "Random jitter fraction (0.2 = ±20%)")
	flag.IntVar(&retries, "retries", 2, "Retry times per file on failure")
	flag.IntVar(&timeout, "timeout", 30, "HTTP timeout in seconds")
	flag.IntVar(&maxWait, "max-wait", 300, "Max adaptive wait seconds (for Retry-After / backoff)")
	flag.Float64Var(&backoff, "backoff", 2.0, "Backoff multiplier when 429/503 or network errors")
	flag.IntVar(&maxErrors, "max-errors", 8, "Abort after this many consecutive errors (polite stop)")
	flag.StringVar(&ext, "ext", "png", "File extension without dot")
	flag.StringVar(&ua, "ua", "qxdl/1.1 gentle (+https://example.local)", "User-Agent header")
	flag.BoolVar(&quiet, "quiet", false, "Quiet mode (less logs)")
	flag.Parse()

	if rawURL == "" || startStr == "" {
		fmt.Println("Usage: qxdl -url <https://.../0001.png> -start 0001 [-end 0077] [-interval 6]")
		os.Exit(2)
	}
	if !strings.HasPrefix(rawURL, "http") {
		exitErr(errors.New("url must start with http/https"))
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		exitErr(err)
	}
	if !isAllDigits(startStr) {
		exitErr(errors.New("start must be digits only (e.g., 0064)"))
	}
	if endStr == "" {
		endStr = startStr
	}
	if !isAllDigits(endStr) {
		exitErr(errors.New("end must be digits only"))
	}

	pad := len(startStr)
	startNum := toDec(startStr)
	endNum := toDec(endStr)
	if endNum < startNum {
		exitErr(fmt.Errorf("end (%d) must be >= start (%d)", endNum, startNum))
	}

	dirURL := path.Dir(u.Path) + "/"
	base := u.Scheme + "://" + u.Host + dirURL
	folder := filepath.Base(path.Dir(u.Path))
	if folder == "." || folder == "/" || folder == "" {
		folder = "downloads"
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		exitErr(err)
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	if !quiet {
		fmt.Printf("BASE: %s\nFOLDER: %s\nSTART: %s  END: %s  PAD: %d  (interval: %ds, jitter: ±%d%%)\n\n",
			base, folder, startStr, endStr, pad, interval, int(jitterFrac*100))
	}

	consecErrors := 0

	for i := startNum; i <= endNum; i++ {
		numStr := fmt.Sprintf("%0*d", pad, i)
		urlNow := fmt.Sprintf("%s%s.%s", base, numStr, ext)
		fileNow := filepath.Join(folder, numStr+"."+ext)

		if _, err := os.Stat(fileNow); err == nil {
			if !quiet {
				fmt.Printf("[skip] %s exists\n", filepath.Base(fileNow))
			}
			// small polite delay even on skip to avoid bursty index scanning
			sleepWithJitter(time.Duration(interval)*time.Second, jitterFrac, quiet)
			continue
		}

		if !quiet {
			fmt.Printf("[get ] %s\n", urlNow)
		}
		res := downloadFile(client, urlNow, fileNow, ua, time.Duration(timeout)*time.Second)

		if res.Err != nil || (res.StatusCode >= 400 && res.StatusCode != 404) {
			consecErrors++
			if !quiet {
				fmt.Printf("[fail] %s (%v, status=%d)\n", urlNow, res.Err, res.StatusCode)
			}
			if consecErrors >= maxErrors {
				fmt.Printf("Too many consecutive errors (%d). Stopping politely.\n", consecErrors)
				break
			}

			// Decide polite wait
			wait := time.Duration(interval) * time.Second
			if res.StatusCode == http.StatusTooManyRequests && res.RetryAfter > 0 {
				wait = res.RetryAfter
			} else if res.StatusCode == http.StatusServiceUnavailable && res.RetryAfter > 0 {
				wait = res.RetryAfter
			} else {
				// exponential backoff based on consecutive errors
				m := math.Pow(backoff, float64(min(consecErrors, 6)))
				wait = time.Duration(float64(wait) * m)
			}
			if wait > time.Duration(maxWait)*time.Second {
				wait = time.Duration(maxWait) * time.Second
			}
			sleepWithJitter(wait, jitterFrac, quiet)
			// retry current i up to 'retries'
			ok := false
			for attempt := 1; attempt <= retries; attempt++ {
				if !quiet {
					fmt.Printf("[retry %d/%d] %s\n", attempt, retries, urlNow)
				}
				res = downloadFile(client, urlNow, fileNow, ua, time.Duration(timeout)*time.Second)
				if res.Err == nil && res.StatusCode == 200 {
					if !quiet {
						fmt.Printf("[ ok ] %s\n", filepath.Base(fileNow))
					}
					ok = true
					consecErrors = 0
					break
				}
				// wait a bit before next retry
				rw := time.Duration(interval) * time.Second
				if res.RetryAfter > 0 {
					rw = res.RetryAfter
				}
				sleepWithJitter(rw, jitterFrac, quiet)
			}
			if !ok {
				// give up on this file, proceed to next politely
				continue
			}
		} else {
			if !quiet {
				fmt.Printf("[ ok ] %s\n", filepath.Base(fileNow))
			}
			consecErrors = 0
		}

		if i < endNum {
			// polite wait between files
			sleepWithJitter(time.Duration(interval)*time.Second, jitterFrac, quiet)
		}
	}

	if !quiet {
		fmt.Println("Done.")
	}
}

func sleepWithJitter(base time.Duration, jitterFrac float64, quiet bool) {
	if base <= 0 {
		return
	}
	// jitter in ±jitterFrac range
	j := time.Duration(float64(base) * jitterFrac)
	delta := time.Duration(rand.Int63n(int64(2*j+1))) - j
	wait := base + delta
	if wait < 0 {
		wait = 0
	}
	if !quiet {
		fmt.Printf("waiting %v...\n", wait.Round(time.Millisecond))
	}
	time.Sleep(wait)
}

func downloadFile(client *http.Client, urlNow, fileNow, ua string, timeout time.Duration) dlResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", urlNow, nil)
	if err != nil {
		return dlResult{Err: err}
	}
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		return dlResult{Err: err}
	}
	defer resp.Body.Close()

	res := dlResult{StatusCode: resp.StatusCode}

	// Parse Retry-After if any (delta-seconds or HTTP date)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if dur, ok := parseRetryAfter(ra); ok {
			res.RetryAfter = dur
		}
	}

	if resp.StatusCode != http.StatusOK {
		return res
	}

	tmp := fileNow + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		res.Err = err
		return res
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		res.Err = err
		return res
	}
	if err := f.Close(); err != nil {
		res.Err = err
		return res
	}
	if err := os.Rename(tmp, fileNow); err != nil {
		res.Err = err
		return res
	}
	return res
}

func parseRetryAfter(v string) (time.Duration, bool) {
	// Try delta-seconds
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	// Try HTTP-date
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func toDec(s string) int {
	i := 0
	for i < len(s) && s[i] == '0' {
		i++
	}
	if i == len(s) {
		return 0
	}
	n := 0
	for ; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func exitErr(err error) {
	fmt.Println("[ERROR]", err)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
