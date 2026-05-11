package main

import (
	"flag"
	"fmt"
	"os"

	"general-mcp/pkg/generalmcp"
)

func main() {
	httpMode := flag.Bool("http", false, "Run in HTTP/SSE mode instead of stdio")
	port := flag.String("port", "8080", "Port for HTTP mode")
	flag.Parse()

	config := generalmcp.ServerConfig{
		HTTPMode: *httpMode,
		Port:     *port,
	}

	srv, err := generalmcp.NewServer(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating server: %v\n", err)
		os.Exit(1)
	}

	srv.PrintStartupInfo()

	if config.HTTPMode {
		err = srv.ServeHTTP()
	} else {
		err = srv.ServeStdio()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
