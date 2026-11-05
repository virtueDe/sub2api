package service

import "context"

// AddAuthCacheInvalidationListener attaches caches whose values can change with
// user/group authorization. Listeners also run for durable outbox deliveries and
// Redis subscription reconnects, including when auth L1 caches are disabled.
func (s *APIKeyService) AddAuthCacheInvalidationListener(listener func()) {
	if s == nil || listener == nil {
		return
	}
	s.authInvalidationListenersMu.Lock()
	s.authInvalidationListeners = append(s.authInvalidationListeners, listener)
	s.authInvalidationListenersMu.Unlock()
	s.StartAuthCacheInvalidationSubscriber(context.Background())
}

func (s *APIKeyService) hasAuthCacheInvalidationListeners() bool {
	s.authInvalidationListenersMu.RLock()
	defer s.authInvalidationListenersMu.RUnlock()
	return len(s.authInvalidationListeners) > 0
}

func (s *APIKeyService) invalidateRelatedAuthCaches() {
	s.authInvalidationListenersMu.RLock()
	listeners := append([]func(){}, s.authInvalidationListeners...)
	s.authInvalidationListenersMu.RUnlock()
	for _, listener := range listeners {
		listener()
	}
}

func (s *GatewayService) InvalidateUserGroupRates() {
	if s != nil && s.userGroupRateResolver != nil {
		s.userGroupRateResolver.Invalidate()
	}
}

func (s *OpenAIGatewayService) InvalidateUserGroupRates() {
	if s != nil && s.userGroupRateResolver != nil {
		s.userGroupRateResolver.Invalidate()
	}
}
