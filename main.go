package main

import (
	"fmt"

	"github.com/pion/webrtc"
)

func main() {
	fmt.Println("hello world")
	offer := webrtc.SessionDescription{}
	// signal.Decode(signal.MustReadStdin(), &offer)

	fmt.Println(offer)
}
