package pool

// Error is a gateway-side failure the gateway hands to the model as an
// isError result, so it follows `maple: <server>: <problem>; <fix>`.
type Error struct {
	Server, Problem, Fix string
}

func (e *Error) Error() string {
	s := "maple: " + e.Server + ": " + e.Problem
	if e.Fix != "" {
		s += "; " + e.Fix
	}
	return s
}
