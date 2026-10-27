package main

import (
	"encoding/json"
	"fmt"
)

// The load generator's accounts. These are LOCAL TEST CREDENTIALS for a
// throwaway server: anyone who reads this file knows them. They exist so a
// load test needs no secret store; never configure them on a server that
// holds anything you care about.
//
// User IDs: the operator is 1; trading users start at firstUser, so they
// never collide with accounts created by hand in a demo.
const (
	adminUser = 1
	firstUser = 1001
)

func adminToken() string     { return "loadgen-local-admin-0001" }
func userToken(i int) string { return fmt.Sprintf("loadgen-local-user-%06d", i) }
func userID(i int) int       { return firstUser + i }

type tokenRecord struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	Admin  bool   `json:"admin,omitempty"`
}

// tokensJSON returns the CEX_API_TOKENS value that matches a workload with
// the given number of users: one admin (to fund accounts) plus each user.
func tokensJSON(users int) (string, error) {
	recs := []tokenRecord{{Token: adminToken(), UserID: fmt.Sprint(adminUser), Admin: true}}
	for i := range users {
		recs = append(recs, tokenRecord{Token: userToken(i), UserID: fmt.Sprint(userID(i))})
	}
	b, err := json.Marshal(recs)
	return string(b), err
}
