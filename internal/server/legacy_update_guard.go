package server

import (
	"context"
	"strings"
	"time"

	"vocat/internal/store"
)

// legacyPendingWindow keeps a just-accepted ATD/ATA visible when the modem's
// CLCC has not listed the call yet. A successful voice CLCC or ATH ends it.
// ATH does not by itself prove the call is gone.
const legacyPendingWindow = 20 * time.Second

// legacySlot is one legacy identity key (config ID or physical ID).
type legacySlot struct {
	active       bool
	known        bool
	pendingUntil time.Time
	generation   string
}

type legacyCandidate struct {
	configID       string
	physicalID     string
	generation     string
	networkEnabled bool
	vowifiPolicy   bool
}

// legacyCallActivity reports a legacy voice call, a just-accepted dial still
// inside its protection window, or an enabled online modem that has never
// returned a successful CLCC. With no store or device inventory, only the
// cache is consulted so unit tests do not need hardware.
func (s *Server) legacyCallActivity() bool {
	if s == nil {
		return false
	}
	if s.legacyCallsForUpdate != nil {
		return s.legacyCallsForUpdate()
	}
	if s.store == nil || s.devices == nil {
		return s.legacyCacheActive()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		return true
	}
	candidates := s.legacyCandidates(configs)
	now := time.Now()
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	for _, candidate := range candidates {
		s.retireLegacyGenerationLocked(candidate)
		ids := s.legacyGroupLocked(candidate.configID, candidate.physicalID)
		if s.legacyGroupActiveLocked(ids, now) {
			return true
		}
		if s.legacyGroupKnownLocked(ids) || candidate.vowifiPolicy || !candidate.networkEnabled {
			continue
		}
		return true
	}
	return false
}

func (s *Server) legacyCandidates(configs []store.Device) []legacyCandidate {
	candidates := make([]legacyCandidate, 0, len(configs))
	for _, config := range configs {
		if isReaderDevice(config) || s.cellularControllerFor(config.ID) != nil {
			continue
		}
		entry, physicalID, present := s.physicalForConfig(config)
		if !present {
			continue
		}
		candidates = append(candidates, legacyCandidate{
			configID:       config.ID,
			physicalID:     physicalID,
			generation:     entry.Candidate.USBGeneration,
			networkEnabled: config.NetworkEnabled,
			vowifiPolicy:   config.VoWiFiEnabled || s.callTransport(config.ID) == "vowifi",
		})
	}
	return candidates
}

func (s *Server) legacyCacheActive() bool {
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	now := time.Now()
	for _, slot := range s.legacySlots {
		if slot.active || legacyPendingOpen(slot, now) {
			return true
		}
	}
	return false
}

func (s *Server) legacyTrackedIDs() map[string]bool {
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	tracked := make(map[string]bool, len(s.legacySlots))
	for id, slot := range s.legacySlots {
		if slot.active {
			tracked[id] = true
		}
	}
	return tracked
}

// captureLegacyRevision samples the cache generation before a CLCC read.
// A dial, answer, or hangup that lands while that read is in flight changes
// the revision and the stale snapshot must not be committed.
func (s *Server) captureLegacyRevision() uint64 {
	if s == nil {
		return 0
	}
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	return s.legacyRevision
}

func (s *Server) noteLegacyVoiceCall(deviceID string) {
	s.noteLegacyAccepted("", deviceID)
}

// noteLegacyAccepted records a successful ATD or ATA. Both identity keys share
// one revision bump and one protection window.
func (s *Server) noteLegacyAccepted(generation string, ids ...string) {
	s.bumpLegacy(generation, true, ids...)
}

// noteLegacyHangup drops the post-dial protection after ATH succeeds. The call
// stays active until a later CLCC snapshot confirms it is gone.
func (s *Server) noteLegacyHangup(ids ...string) {
	s.bumpLegacy("", false, ids...)
}

func (s *Server) bumpLegacy(generation string, accepted bool, ids ...string) {
	if s == nil {
		return
	}
	clean := legacyIDs(ids...)
	if len(clean) == 0 {
		return
	}
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	s.legacyRevision++
	s.linkLegacyLocked(clean)
	until := time.Time{}
	if accepted {
		until = time.Now().Add(legacyPendingWindow)
	}
	for _, id := range s.legacyGroupLocked(clean...) {
		slot := s.legacySlotValueLocked(id)
		if accepted {
			slot.active = true
			slot.pendingUntil = until
			if generation != "" {
				slot.generation = generation
			}
		} else {
			slot.pendingUntil = time.Time{}
		}
		s.putLegacySlotLocked(id, slot)
	}
}

// observeLegacyCLCC commits a snapshot that was just read. Failed queries are
// ignored so they neither create nor erase state.
func (s *Server) observeLegacyCLCC(deviceID string, calls []map[string]any, queryOK bool) {
	if s == nil || !queryOK {
		return
	}
	s.observeLegacyCLCCAtRevision([]string{deviceID}, calls, s.captureLegacyRevision())
}

// observeLegacyCLCCAtRevision commits one CLCC snapshot for every aliased key
// when revision is still current. A mismatched revision is discarded.
func (s *Server) observeLegacyCLCCAtRevision(ids []string, calls []map[string]any, revision uint64) {
	s.commitLegacyCLCC(ids, calls, revision, "")
}

func (s *Server) commitLegacyCLCC(ids []string, calls []map[string]any, revision uint64, generation string) {
	if s == nil {
		return
	}
	active := false
	for _, call := range calls {
		if clccVoiceBlocksUpdate(call) {
			active = true
			break
		}
	}
	s.legacyCallMu.Lock()
	defer s.legacyCallMu.Unlock()
	if revision != s.legacyRevision {
		return
	}
	clean := legacyIDs(ids...)
	if len(clean) == 0 {
		return
	}
	s.retireLegacyGenerationLocked(legacyCandidate{configID: clean[0], physicalID: clean[len(clean)-1], generation: generation})
	s.linkLegacyLocked(clean)
	now := time.Now()
	for _, id := range s.legacyGroupLocked(clean...) {
		slot := s.legacySlotValueLocked(id)
		if generation != "" {
			slot.generation = generation
		}
		if active {
			slot.known = true
			slot.active = true
			slot.pendingUntil = time.Time{}
			s.putLegacySlotLocked(id, slot)
			continue
		}
		if legacyPendingOpen(slot, now) {
			continue
		}
		slot.known = true
		slot.active = false
		slot.pendingUntil = time.Time{}
		s.putLegacySlotLocked(id, slot)
	}
}

func (s *Server) legacySlotValueLocked(id string) legacySlot {
	if s.legacySlots == nil {
		s.legacySlots = map[string]legacySlot{}
	}
	return s.legacySlots[id]
}

func (s *Server) putLegacySlotLocked(id string, slot legacySlot) {
	if s.legacySlots == nil {
		s.legacySlots = map[string]legacySlot{}
	}
	s.legacySlots[id] = slot
}

// Retire every alias of a replaced USB instance. The current modem must still
// establish its own call state; absence of the old call does not mean it is idle.
func (s *Server) retireLegacyGenerationLocked(candidate legacyCandidate) {
	if candidate.generation == "" {
		return
	}
	ids := s.legacyGroupLocked(candidate.configID, candidate.physicalID)
	changed := false
	for _, id := range ids {
		if slot, ok := s.legacySlots[id]; ok && slot.generation != "" && slot.generation != candidate.generation {
			changed = true
			break
		}
	}
	if !changed {
		return
	}
	s.legacyRevision++
	for _, id := range ids {
		delete(s.legacySlots, id)
		delete(s.legacyPeers, id)
	}
	for _, peers := range s.legacyPeers {
		for _, id := range ids {
			delete(peers, id)
		}
	}
}

// Sample the physical inventory before the AT query and check it again before
// publishing its result. A result from an unplugged instance cannot describe
// the replacement, even when the logical ID and serial device path are reused.
func (s *Server) legacyPhysicalGeneration(physicalID string) (string, bool) {
	if s == nil || s.devices == nil {
		return "", false
	}
	entry, err := s.devices.Get(physicalID)
	if err != nil || !entry.Discovered {
		return "", false
	}
	return entry.Candidate.USBGeneration, true
}

func (s *Server) legacyPhysicalUnchanged(physicalID, generation string, present bool) bool {
	current, online := s.legacyPhysicalGeneration(physicalID)
	return present && online && current == generation
}

func (s *Server) legacyGroupActiveLocked(ids []string, now time.Time) bool {
	for _, id := range ids {
		slot, ok := s.legacySlots[id]
		if ok && (slot.active || legacyPendingOpen(slot, now)) {
			return true
		}
	}
	return false
}

func (s *Server) legacyGroupKnownLocked(ids []string) bool {
	for _, id := range ids {
		if slot, ok := s.legacySlots[id]; ok && slot.known {
			return true
		}
	}
	return false
}

func (s *Server) linkLegacyLocked(ids []string) {
	if s.legacyPeers == nil {
		s.legacyPeers = map[string]map[string]struct{}{}
	}
	for _, left := range ids {
		if s.legacyPeers[left] == nil {
			s.legacyPeers[left] = map[string]struct{}{}
		}
		for _, right := range ids {
			if left == right {
				continue
			}
			s.legacyPeers[left][right] = struct{}{}
		}
	}
}

func (s *Server) legacyGroupLocked(ids ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	var walk func(string)
	walk = func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
		for peer := range s.legacyPeers[id] {
			walk(peer)
		}
	}
	for _, id := range ids {
		walk(id)
	}
	return out
}

func legacyIDs(ids ...string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func legacyPendingOpen(slot legacySlot, now time.Time) bool {
	return !slot.pendingUntil.IsZero() && now.Before(slot.pendingUntil)
}

func clccVoiceBlocksUpdate(call map[string]any) bool {
	mode, modeOK := call["mode"].(int)
	state, stateOK := call["state"].(int)
	if !modeOK || !stateOK || mode != 0 {
		return false
	}
	return state >= 0 && state <= 5
}
