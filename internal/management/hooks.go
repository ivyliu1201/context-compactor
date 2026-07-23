package management

import (
	"fmt"
)

func addOwnedHooks(
	document map[string]any,
	host Host,
	command string,
	commandWindows string,
) error {
	installed := hostInstalled{
		Command:        command,
		CommandWindows: commandWindows,
	}
	if _, err := removeOwnedHooks(document, installed); err != nil {
		return err
	}
	hooks, err := hooksObject(document)
	if err != nil {
		return err
	}
	for _, event := range requiredHookEvents {
		groups, err := hookGroups(hooks, event)
		if err != nil {
			return err
		}
		handler := map[string]any{
			"type":    "command",
			"command": command,
			"timeout": hookTimeout,
		}
		if host == HostCodex {
			handler["statusMessage"] = "Loading bounded project context"
			if commandWindows != "" {
				handler["commandWindows"] = commandWindows
			}
		}
		groups = append(groups, map[string]any{
			"hooks": []any{handler},
		})
		hooks[event] = groups
	}
	return nil
}

func countOwnedHooks(
	document map[string]any,
	installed hostInstalled,
) (map[string]int, error) {
	hooks, err := hooksObjectRead(document)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(requiredHookEvents))
	for _, event := range requiredHookEvents {
		groups, err := hookGroupsRead(hooks, event)
		if err != nil {
			return nil, err
		}
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s entries must be JSON objects", event)
			}
			rawHandlers, found := group["hooks"]
			if !found {
				return nil, fmt.Errorf("hooks.%s entry is missing hooks", event)
			}
			handlers, ok := rawHandlers.([]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s handlers must be a JSON array", event)
			}
			for _, rawHandler := range handlers {
				handler, ok := rawHandler.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("hooks.%s handler must be a JSON object", event)
				}
				if ownedHandler(handler, installed) {
					counts[event]++
				}
			}
		}
	}
	return counts, nil
}

func removeOwnedHooks(
	document map[string]any,
	installed hostInstalled,
) (int, error) {
	rawHooks, found := document["hooks"]
	if !found {
		return 0, nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("hooks must be a JSON object")
	}
	removed := 0
	for _, event := range requiredHookEvents {
		groups, err := hookGroupsRead(hooks, event)
		if err != nil {
			return 0, err
		}
		keptGroups := make([]any, 0, len(groups))
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return 0, fmt.Errorf("hooks.%s entries must be JSON objects", event)
			}
			rawHandlers, found := group["hooks"]
			if !found {
				return 0, fmt.Errorf("hooks.%s entry is missing hooks", event)
			}
			handlers, ok := rawHandlers.([]any)
			if !ok {
				return 0, fmt.Errorf("hooks.%s handlers must be a JSON array", event)
			}
			keptHandlers := make([]any, 0, len(handlers))
			for _, rawHandler := range handlers {
				handler, ok := rawHandler.(map[string]any)
				if !ok {
					return 0, fmt.Errorf("hooks.%s handler must be a JSON object", event)
				}
				if ownedHandler(handler, installed) {
					removed++
					continue
				}
				keptHandlers = append(keptHandlers, handler)
			}
			if len(keptHandlers) == 0 {
				continue
			}
			group["hooks"] = keptHandlers
			keptGroups = append(keptGroups, group)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if len(hooks) == 0 {
		delete(document, "hooks")
	}
	return removed, nil
}

func ownedHandler(handler map[string]any, installed hostInstalled) bool {
	handlerType, _ := handler["type"].(string)
	command, _ := handler["command"].(string)
	if handlerType != "command" || command != installed.Command {
		return false
	}
	if installed.CommandWindows == "" {
		return true
	}
	commandWindows, _ := handler["commandWindows"].(string)
	return commandWindows == installed.CommandWindows
}

func hooksObject(document map[string]any) (map[string]any, error) {
	raw, found := document["hooks"]
	if !found {
		hooks := make(map[string]any)
		document["hooks"] = hooks
		return hooks, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks must be a JSON object")
	}
	return hooks, nil
}

func hooksObjectRead(document map[string]any) (map[string]any, error) {
	raw, found := document["hooks"]
	if !found {
		return make(map[string]any), nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks must be a JSON object")
	}
	return hooks, nil
}

func hookGroups(hooks map[string]any, event string) ([]any, error) {
	groups, err := hookGroupsRead(hooks, event)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		return make([]any, 0), nil
	}
	return groups, nil
}

func hookGroupsRead(hooks map[string]any, event string) ([]any, error) {
	raw, found := hooks[event]
	if !found {
		return nil, nil
	}
	groups, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("hooks.%s must be a JSON array", event)
	}
	return groups, nil
}
