package api

// HoldAnalytics takes every analytics slot, as long-running reports would, until release.
func (s *Server) HoldAnalytics() (release func()) {
	for range cap(s.anaSlots) {
		s.anaSlots <- struct{}{}
	}
	return func() {
		for range cap(s.anaSlots) {
			<-s.anaSlots
		}
	}
}
