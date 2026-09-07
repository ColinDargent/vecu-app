package web

import "github.com/colindargent/vecu/server/auth"

func hashJeton(j string) string { return auth.HashToken(j) }
