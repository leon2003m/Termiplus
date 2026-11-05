package gateway

import "termiplus/internal/naming"

// streamTarget returns the running desktop instance called name if it belongs
// to userID, or nil.
func (m *manager) streamTarget(name, userID string) *instance {
	if !naming.IsManagedContainer(name) || userID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[name]
	if inst == nil || !inst.desktop || inst.userID != userID {
		return nil
	}
	return inst
}
