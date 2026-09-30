package forwarder

import "strconv"

// AssertAcyclicRoutes rejects enabled routes whose chat-level graph contains a cycle.
func AssertAcyclicRoutes(routes []Route) error {
	order := make([]int64, 0)
	index := map[int64]int{}
	edges := map[int64][]int64{}
	seenEdge := map[int64]map[int64]struct{}{}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		source := route.Source.ChatID
		dest := route.Destination.ChatID
		if _, ok := index[source]; !ok {
			index[source] = len(order)
			order = append(order, source)
		}
		if seenEdge[source] == nil {
			seenEdge[source] = map[int64]struct{}{}
		}
		if _, ok := seenEdge[source][dest]; ok {
			continue
		}
		seenEdge[source][dest] = struct{}{}
		edges[source] = append(edges[source], dest)
	}

	visiting := map[int64]struct{}{}
	visited := map[int64]struct{}{}
	path := make([]int64, 0)
	var visit func(int64) error
	visit = func(chatID int64) error {
		if _, ok := visiting[chatID]; ok {
			start := 0
			for i, item := range path {
				if item == chatID {
					start = i
					break
				}
			}
			cycle := append(append([]int64{}, path[start:]...), chatID)
			parts := make([]string, len(cycle))
			for i, item := range cycle {
				parts[i] = strconv.FormatInt(item, 10)
			}
			rendered := ""
			for i, part := range parts {
				if i > 0 {
					rendered += " -> "
				}
				rendered += part
			}
			return RouteCycleError{msg: "forwarding route cycle detected: " + rendered}
		}
		if _, ok := visited[chatID]; ok {
			return nil
		}
		visiting[chatID] = struct{}{}
		path = append(path, chatID)
		for _, destination := range edges[chatID] {
			if err := visit(destination); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		delete(visiting, chatID)
		visited[chatID] = struct{}{}
		return nil
	}
	for _, source := range order {
		if err := visit(source); err != nil {
			return err
		}
	}
	return nil
}
