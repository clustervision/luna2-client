package engine

import "reflect"

// FanIn merges events from a read-only runner channel and any number of
// auxiliary producer channels into a single output channel. The output
// closes when src closes; an auxiliary channel that closes is dropped from
// the merge (the others keep flowing). Uses reflect.Select so it handles N
// auxiliary channels correctly (the previous version only watched aux[0]).
//
// Buffer is the capacity of the returned channel.
func FanIn(buffer int, src <-chan InstallerEvent, aux ...<-chan InstallerEvent) <-chan InstallerEvent {
	if buffer < 16 {
		buffer = 16
	}
	out := make(chan InstallerEvent, buffer)
	go func() {
		defer close(out)
		// cases[0] is always src; the rest are aux channels.
		cases := make([]reflect.SelectCase, 0, len(aux)+1)
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(src)})
		for _, a := range aux {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a)})
		}
		for {
			chosen, val, ok := reflect.Select(cases)
			if chosen == 0 {
				if !ok {
					return // src closed: installer finished, stop merging.
				}
				out <- val.Interface().(InstallerEvent)
				continue
			}
			if !ok {
				// An aux channel closed: drop it so we don't busy-spin on a
				// dead channel. Indices >0 shift down; src stays at 0.
				cases = append(cases[:chosen], cases[chosen+1:]...)
				continue
			}
			out <- val.Interface().(InstallerEvent)
		}
	}()
	return out
}
