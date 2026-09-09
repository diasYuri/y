package ai

import "fmt"

// MigrateMessage upgrades a wire message from a supported older schema to the
// current schema. Migration is explicit so stores can normalize data before
// validation and before handing it to a provider.
func MigrateMessage(message Message) (Message, error) {
	if message.SchemaVersion < 0 || message.SchemaVersion > CurrentSchemaVersion {
		return Message{}, fmt.Errorf("ai: unsupported message schema version %d", message.SchemaVersion)
	}
	if message.SchemaVersion == 0 {
		message.SchemaVersion = CurrentSchemaVersion
	}
	return message, nil
}

// MigrateContext upgrades a provider context and every message it contains.
func MigrateContext(input Context) (Context, error) {
	if input.SchemaVersion < 0 || input.SchemaVersion > CurrentSchemaVersion {
		return Context{}, fmt.Errorf("ai: unsupported context schema version %d", input.SchemaVersion)
	}
	if input.SchemaVersion == 0 {
		input.SchemaVersion = CurrentSchemaVersion
	}
	for i := range input.Messages {
		message, err := MigrateMessage(input.Messages[i])
		if err != nil {
			return Context{}, err
		}
		input.Messages[i] = message
	}
	return input, nil
}
