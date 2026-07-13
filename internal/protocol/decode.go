package protocol

import (
	"encoding/json"
	"fmt"
	"io"
)

func DecodeTransientEvent(reader io.Reader) (TransientEvent, error) {
	event, err := decodeStrict[TransientEvent](reader)
	if err != nil {
		return TransientEvent{}, fmt.Errorf("decode transient event: %w", err)
	}
	if err := ValidateTransientEvent(event); err != nil {
		return TransientEvent{}, err
	}
	return event, nil
}

func DecodeMutationBatch(reader io.Reader) (MutationBatch, error) {
	batch, err := decodeStrict[MutationBatch](reader)
	if err != nil {
		return MutationBatch{}, fmt.Errorf("decode mutation batch: %w", err)
	}
	if err := ValidateMutationBatch(batch); err != nil {
		return MutationBatch{}, err
	}
	return batch, nil
}

func decodeStrict[T any](reader io.Reader) (T, error) {
	var value T
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("input contains more than one JSON value")
		}
		return value, fmt.Errorf("read trailing JSON data: %w", err)
	}
	return value, nil
}
