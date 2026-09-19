package main

import (
	"fmt"
	"log"
	"context"

	"Kho-beng-nae/wordlist"
	"Kho-beng-nae/lib/scanner"
	//"Kho-beng-nae/util"
)

func main() {
	wl, err := wordlist.Load("wordlist/wordlist.txt")

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Loaded paths:", wl.Len())

	s := scanner.NewFastHTTP(
		"https://tc-nextjs-tawny.vercel.app/",
		100,
		wl,
	)

	results := s.Scan(context.Background())

	for result := range results {
		path := wl.Get(int(result.PathIndex))

		if result.Err != nil {
			fmt.Printf(
				"[ERR] /%s -> %v\n",
				path,
				result.Err,
			)
			continue
		}

		fmt.Printf(
			"[%d] /%s size=%d time=%v\n",
			result.StatusCode,
			path,
			result.Size,
			result.Duration,
		)
	}
}