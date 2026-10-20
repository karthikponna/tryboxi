package server

import "google.golang.org/protobuf/proto"

func unmarshal(b []byte, m proto.Message) error { return proto.Unmarshal(b, m) }
