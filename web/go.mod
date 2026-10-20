// This file only stops the Go tool from walking web/node_modules (some npm
// packages ship .go files). The frontend is not a Go module.
module github.com/DonBlack-117/watchkeeper/web

go 1.26
