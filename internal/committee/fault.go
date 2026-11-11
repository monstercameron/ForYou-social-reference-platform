package committee

import (
	"errors"

	"foryou.local/foryou/internal/settlement"
)

// Fault is the first listed settlement refusal and whether public objects prove it.
type Fault struct {
	Reason     settlement.Reason
	Provable   bool
	dependency dependency
}

type dependency uint8

const (
	dependencyNone dependency = iota
	dependencySuccession
	dependencyDeclaration
	dependencySchedule
	dependencyPredecessor
)

func fault(reason settlement.Reason, provable bool) *Fault {
	return &Fault{Reason: reason, Provable: provable}
}

func mapLinkFault(err error) *Fault {
	var link *settlement.LinkError
	if !errors.As(err, &link) {
		return nil
	}
	switch link.Kind {
	case settlement.LinkKey:
		f := fault(settlement.ReasonUnknownProviderKey, false)
		f.dependency = dependencySuccession
		return f
	case settlement.LinkSuccession:
		return fault(settlement.ReasonUnknownProviderKey, true)
	case settlement.LinkChain:
		return fault(settlement.ReasonBadChain, true)
	case settlement.LinkPeriod:
		return fault(settlement.ReasonBadPeriod, true)
	default:
		return nil
	}
}
