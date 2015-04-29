// Copyright 2015 Philipp Franke. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.
//

package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/garyburd/go-oauth/oauth"
)

// Useful flags
var (
	query      = flag.String("q", "", "Search for tweets referencing the given q")
	language   = flag.String("lang", "en", "Restricts tweets to the given lang")
	until      = flag.String("until", "", "Restricts tweets sent before the given date. (YYYY-MM-DD)")
	maxID      = flag.Int64("max_id", 0, "Restricts tweets with an ID less than or equal to the given ID")
	sinceID    = flag.Int64("since_id", 0, "Restricts tweets with an ID greater than the given ID")
	count      = flag.Int("count", 15, "Number of tweets returned per request")
	resultType = flag.String("result_type", "mixed", "recent: only most recent tweets, popular: only most popular tweets, mixed: both")
	token      = flag.String("token", "", "Consumer Key")
	secret     = flag.String("secret", "", "Consumer Secret")
)

// oauth Client
var authClient *oauth.Client

// Mapping all available resultTypes
var resultTypes = map[string]bool{
	"mixed":   true,
	"recent":  true,
	"popular": true,
}

// Tweet represents a twitter post
type tweet struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	User      user   `json:"user"`
	Text      string `json:"text"`
}

// User represents a twitter user
type user struct {
	ScreenName string `json:"screen_name"`
}

// Result represents a search result
type result struct {
	Metadata metadata `json:"search_metadata"`
	Statuses []tweet  `json:"statuses"`
}

// Metadata represents search metadata
type metadata struct {
	NextResult string `json:"next_results"`
}

func main() {
	flag.Parse()

	// Twitter Auth
	authClient = &oauth.Client{
		Credentials: oauth.Credentials{
			Token:  *token,
			Secret: *secret,
		},
	}

	// Prepare twitter query
	params := make(url.Values)

	// Check Query
	if *query == "" {
		log.Fatal("q is required!")
	}

	if len(*query) > 500 {
		log.Fatalf("q has too many characters: %d", len(*query))
	}
	params.Set("q", *query)

	// Check Language
	if len(*language) != 0 && len(*language) != 2 {
		log.Fatalf("lang has too many characters: %d; (ISO 639-1)", len(*language))
	} else {
		params.Set("lang", *language)
	}

	// Check until
	if *until != "" && !correctDate(*until) {
		log.Println("until couldn't be parsed! Ignore until")
	} else {
		params.Set("until", *until)
	}

	// Check count
	if *count < 0 || *count > 100 {
		log.Printf("Count is not between 0 and 100: %d! Use default: 15", *count)
		*count = 15
	}
	params.Set("count", strconv.Itoa(*count))

	// Check result_type
	*resultType = strings.ToLower(*resultType)
	if !resultTypes[*resultType] {
		log.Printf("result_type is invalid: %s! Use default: mixed", *resultType)
		*resultType = "mixed"
	}
	params.Set("result_type", *resultType)

	// Check max_id
	if *maxID != 0 {
		params.Set("max_id", strconv.Itoa(int(*maxID)))
	}

	// Check since_id
	if *sinceID != 0 {
		params.Set("since_id", strconv.Itoa(int(*sinceID)))
	}

	rawParam := params.Encode()

	// Graceful stop
	twitterStopChan := make(chan struct{}, 1)
	csvStopChan := make(chan struct{}, 1)
	stop := false
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signalChan
		log.Println("Stopping...")
		stop = true
	}()

	// Chan for tweets
	pages := make(chan []tweet)

	// CSV
	go func() {
		defer func() {
			csvStopChan <- struct{}{}
		}()

		file, err := os.Create("output.csv")
		if err != nil {
			log.Printf("Could create csv file: %v", err)
			return
		}
		w := csv.NewWriter(file)

		w.Write([]string{"ID", "Created at", "Screen Name", "Tweet"})

		for page := range pages {
			for _, tweet := range page {

				r := []string{
					strconv.Itoa(int(tweet.ID)),
					tweet.CreatedAt,
					tweet.User.ScreenName,
					tweet.Text,
				}

				if err := w.Write(r); err != nil {
					log.Printf("Couldn't write tweet: %v", err)
					continue
				}
			}

			w.Flush()

			if err := w.Error(); err != nil {
				log.Printf("Couldn't write: %v", err)
				break
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			log.Printf("Couldn't write: %v", err)
		}

		log.Println("Stopped writing to CSV.")
	}()

	// Twitter
	go func() {
		defer func() {
			twitterStopChan <- struct{}{}
			log.Println("Stopped collecting tweets.")
		}()

		u, _ := url.Parse("https://api.twitter.com/1.1/search/tweets.json")

		for {
			if stop {
				return
			}
			time.Sleep(500 * time.Millisecond)

			// Set query
			u.RawQuery = rawParam

			req, err := http.NewRequest("GET", u.String(), nil)
			if err != nil {
				log.Println("Couldn't create request:", err)
			}
			// Add oauth header
			req.Header.Set("Authorization", authClient.AuthorizationHeader(nil, "GET", u, nil))

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Println("Error getting response:", err)
				continue
			}

			// Wait if limit is reached
			if resp.StatusCode == 429 {
				resetTime := resp.Header.Get("X-Rate-Limit-Reset")
				sec, _ := strconv.ParseInt(resetTime, 10, 64)
				wait := time.Since(time.Unix(sec, 0))
				log.Printf("Reached rate limit wait: %s", wait)
				time.Sleep(wait)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				b, _ := ioutil.ReadAll(resp.Body)
				log.Printf("StatusCode = %d, Body: %s", resp.StatusCode, string(b))
				continue
			}

			d := json.NewDecoder(resp.Body)
			var result result
			if err := d.Decode(&result); err == nil {
				if len(result.Statuses) > 0 {
					log.Printf("Collected %d tweets", len(result.Statuses))
					pages <- result.Statuses
				}
				if result.Metadata.NextResult != "" {
					// Set new query with maxid
					rawParam = result.Metadata.NextResult[1:]
				} else {
					break
				}
			} else {
				break
			}
		}
	}()

	<-twitterStopChan
	close(pages)
	<-csvStopChan
}

// correctDate checks if given str is formatted as YYYY-MM-DD and valid.
func correctDate(str string) bool {
	_, err := time.Parse("2006-01-02", str)
	if err != nil {
		return false
	}
	return true
}
