// Command basic connects to a GL.iNet router and prints its identity.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/basic
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gogl "github.com/gherlein/gogl/src"
)

func main() {
	client, err := gogl.New(gogl.Config{
		Host:     os.Getenv("GL_ROUTER_IP"),
		Password: os.Getenv("GL_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// New did not contact the router; this first call is what authenticates.
	info, err := client.System().Info(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("endpoint: %s\n", client.Endpoint())
	fmt.Printf("model:    %s\n", info.Model)
	fmt.Printf("firmware: %s\n", info.Firmware)
	fmt.Printf("uptime:   %d seconds\n", info.Uptime)
}
