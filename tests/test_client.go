package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

func main() {
	baseURL := "http://localhost:8080"
	fmt.Println("Starting Load Test...")

	client := &http.Client{}

	// 1. FILL MEMTABLE (Trigger Flush)
	// We use explicit PUT requests now
	for i := 0; i < 6000; i++ {
		key := fmt.Sprintf("key-%05d", i)
		val := fmt.Sprintf("val-%05d", i)
		url := fmt.Sprintf("%s/put?key=%s&val=%s", baseURL, key, val)

		// Create a proper PUT request
		req, err := http.NewRequest(http.MethodPut, url, nil)
		if err != nil {
			panic(err)
		}

		resp, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		// Always close body to prevent resource leaks
		resp.Body.Close()

		if i%200 == 0 {
			fmt.Printf("Inserted %d keys...\n", i)
		}
	}
	fmt.Println("Finished Inserting. MemTable should have flushed to SSTable.")

	// Give the FlushWorker a moment to write the file
	time.Sleep(1 * time.Second)

	// 2. VERIFY SSTABLE READ (Reading an old key)
	// 'key-00010' was inserted early, so it is likely in the SSTable (Disk), not MemTable.
	fmt.Println("\n--- Testing Read (Should find in SSTable) ---")

	// http.Get is fine here because the endpoint is GET /get
	resp, err := http.Get(baseURL + "/get?key=key-00010")
	if err != nil {
		fmt.Printf("Read Error: %v\n", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("Response: %s\n", string(body))
	}

	// 3. VERIFY BLOOM FILTER (Reading a missing key)
	fmt.Println("\n--- Testing Bloom Filter (Should skip Disk) ---")
	// Look at your SERVER LOGS after running this
	resp, err = http.Get(baseURL + "/get?key=key-99999-missing")
	if err == nil {
		resp.Body.Close()
	}
	fmt.Println("Check server logs for '[Bloom Filter] Blocked key' message!")
}
