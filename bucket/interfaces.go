package bucket

import (
	"fmt"
	"github.com/mateusz/tempomat/lib/config"
	"net/http"
)

type Entry interface {
	fmt.Stringer
	Credits() float64
}

// stupid name
type Bucketable interface {
	Title() string // maybe this should be fmt.Stringer?
	Entries() map[string]Entry
	Register(r *http.Request, cost float64)
	Threshold() float64
	SetConfig(config.Config)
}
