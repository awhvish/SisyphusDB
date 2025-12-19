package main

import (
	"KV-Store/api"
	"fmt"
	"net/http"
)

const PORT = ":8080"

func main() {
	mux := http.NewServeMux()

	server := api.NewServer()

	mux.HandleFunc("PUT /put", server.HandlePut)
	mux.HandleFunc("GET /get", server.HandleGet)
	mux.HandleFunc("DELETE /delete", server.HandleDelete)

	fmt.Println("Server is running on port: ", PORT)

	if err := http.ListenAndServe(PORT, mux); err != nil {
		fmt.Println("Server Error: ", err)
	}
}
