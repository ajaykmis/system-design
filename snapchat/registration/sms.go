package main

import (
	"fmt"
	"log"
)

// SMSProvider is the interface for sending verification codes.
type SMSProvider interface {
	Send(phone, code string) error
}

// MockSMS logs the code to stdout instead of sending a real SMS.
type MockSMS struct{}

func (m *MockSMS) Send(phone, code string) error {
	log.Printf("📱 [MOCK SMS] Sending code %s to %s", code, phone)
	fmt.Printf("\n=== VERIFICATION CODE ===\n  Phone: %s\n  Code:  %s\n=========================\n\n", phone, code)
	return nil
}
