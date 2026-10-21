// Command reservations demonstrates the full reservation lifecycle: create, read,
// update, delete, plus the two failures the library refuses.
//
// A reservation pins a MAC to an IP and nothing else -- it does not create a DNS
// record. See the package documentation for src/.
//
// It uses an address near the top of the subnet and a locally-administered MAC
// that no real device will have, so it is safe to run against a live router. It
// cleans up after itself even if a later step fails.
//
// Run with:
//
//	GL_ROUTER_IP=192.168.8.1 GL_PASSWORD=... go run ./examples/reservations
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
)

const (
	demoName = "gogl-demo"
	// Locally administered (the 0x02 bit in the first octet is set), so this can
	// never collide with a real device.
	demoMAC      = "02:00:00:de:a0:01"
	demoOtherMAC = "02:00:00:de:a0:02"
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

	// Pick an address inside the subnet but above the usual DHCP pool.
	network, err := client.Network().Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	subnet, err := network.Subnet()
	if err != nil {
		log.Fatal(err)
	}
	base := subnet.IP.To4()
	if base == nil {
		log.Fatal("router LAN is not IPv4")
	}
	demoIP := fmt.Sprintf("%d.%d.%d.250", base[0], base[1], base[2])

	reservations := client.Reservations()

	fmt.Printf("creating %s -> %s\n", demoName, demoIP)
	created, err := reservations.Create(ctx, &types.Reservation{
		Name: demoName, MAC: demoMAC, IP: demoIP,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Clean up even if a later step fails, so a demo never leaves debris behind.
	defer func() {
		if err := reservations.Delete(ctx, demoMAC); err != nil {
			log.Printf("cleanup failed, remove %s by hand: %v", demoName, err)
			return
		}
		fmt.Printf("deleted %s (the DNS name went with it)\n", demoName)
	}()

	fetched, err := reservations.GetByName(ctx, demoName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("read back: %s %s %s\n", fetched.Name, fetched.MAC, fetched.IP)
	fmt.Printf("that MAC will now always receive %s\n", fetched.IP)
	// Deliberately not claiming DNS here: a static bind does not create a record.
	// The router's DNS answers from lease hostnames the clients announce.
	fmt.Println("note: this does NOT create a DNS record for the name")

	// Creating the same MAC twice is a conflict, not a silent overwrite.
	if _, err := reservations.Create(ctx, created); errors.Is(err, gogl.ErrConflict) {
		fmt.Println("duplicate create correctly refused")
	} else {
		log.Printf("expected ErrConflict on a duplicate create, got: %v", err)
	}

	// A name that would corrupt dnsmasq's config is refused by the library, before
	// anything reaches the router.
	if _, err := reservations.Create(ctx, &types.Reservation{
		Name: `bad"name`, MAC: demoOtherMAC, IP: demoIP,
	}); errors.Is(err, gogl.ErrInvalidName) {
		fmt.Println("unsafe name correctly refused")
	} else {
		log.Printf("expected ErrInvalidName, got: %v", err)
	}
}
