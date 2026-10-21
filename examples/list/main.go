// Command list prints the router's network configuration, reservations, and
// connected clients.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/list
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

	ctx := context.Background()

	network, err := client.Network().Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	subnet, err := network.Subnet()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("LAN:    %s\n", subnet)
	fmt.Printf("pool:   %s - %s (%d addresses)\n", network.DHCPStart, network.DHCPStop, network.PoolSize())
	fmt.Printf("lease:  %s\n", network.DHCPLease)
	fmt.Printf("iface:  %s\n", network.Interface)

	reservations, err := client.Reservations().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%d reservations (each one is also a DNS record):\n", len(reservations))
	for _, r := range reservations {
		fmt.Printf("  %-20s %s  %s\n", r.Name, r.MAC, r.IP)
	}

	clients, err := client.Clients().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%d clients:\n", len(clients))
	for i := range clients {
		c := clients[i]
		wiring := c.Band()
		if c.IsWired() {
			wiring = "wired"
		}
		fmt.Printf("  %-20s %-18s %-15s %s\n", c.Hostname(), c.MAC, c.IP, wiring)
	}
}
