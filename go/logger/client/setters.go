package logging

import (
	"trie-trunk/go/codec"
	Keys "trie-trunk/go/logger/client/keys"
)

func (b *Builder) Category(value Category) *Builder {
	return b.mutate(func(e *Entry) { e.Category = value; e.present |= 1 << Keys.Category })
}

func (b *Builder) Level(value Level) *Builder {
	return b.mutate(func(e *Entry) { e.Level = value; e.present |= 1 << Keys.Level })
}

func (b *Builder) HttpStatus(value uint16) *Builder {
	return b.mutate(func(e *Entry) { e.HttpStatus = value; e.present |= 1 << Keys.HttpStatus })
}

func (b *Builder) ResponseBytes(value uint64) *Builder {
	return b.mutate(func(e *Entry) { e.ResponseBytes = value; e.present |= 1 << Keys.ResponseBytes })
}

func (b *Builder) Version(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Version = codec.LimitText(value, b.state.logger.config.MaxTextBytes) })
}

func (b *Builder) ClientTransactionId(value string) *Builder {
	return b.mutate(func(e *Entry) { e.ClientTransactionId = codec.LimitText(value, b.state.logger.config.MaxTextBytes) })
}

func (b *Builder) ClientId(value string) *Builder {
	return b.mutate(func(e *Entry) { e.ClientId = codec.LimitText(value, b.state.logger.config.MaxTextBytes) })
}

func (b *Builder) Uri(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Uri = codec.LimitText(value, b.state.logger.config.MaxTextBytes) })
}

func (b *Builder) ClientIp(value string) *Builder {
	return b.mutate(func(e *Entry) { e.ClientIp = codec.LimitText(value, b.state.logger.config.MaxTextBytes) })
}

func (b *Builder) Service(value ServiceId) *Builder {
	return b.mutate(func(e *Entry) { e.Service = codec.Ref(uint16(value)) })
}

func (b *Builder) ServiceText(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Service = codec.Text(codec.LimitText(value, b.state.logger.config.MaxTextBytes)) })
}

func (b *Builder) Resource(value ResourceId) *Builder {
	return b.mutate(func(e *Entry) { e.Resource = codec.Ref(uint16(value)) })
}

func (b *Builder) ResourceText(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Resource = codec.Text(codec.LimitText(value, b.state.logger.config.MaxTextBytes)) })
}

func (b *Builder) Operation(value OperationId) *Builder {
	return b.mutate(func(e *Entry) { e.Operation = codec.Ref(uint16(value)) })
}

func (b *Builder) OperationText(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Operation = codec.Text(codec.LimitText(value, b.state.logger.config.MaxTextBytes)) })
}

func (b *Builder) Message(value MessageId) *Builder {
	return b.mutate(func(e *Entry) { e.Message = codec.Ref(uint16(value)) })
}

func (b *Builder) MessageText(value string) *Builder {
	return b.mutate(func(e *Entry) { e.Message = codec.Text(codec.LimitText(value, b.state.logger.config.MaxTextBytes)) })
}

func (b *Builder) HttpMethod(value HttpMethod) *Builder {
	return b.mutate(func(e *Entry) { e.HttpMethod = codec.Ref(uint16(value)) })
}

func (b *Builder) HttpMethodText(value string) *Builder {
	return b.mutate(func(e *Entry) { e.HttpMethod = codec.Text(codec.LimitText(value, b.state.logger.config.MaxTextBytes)) })
}
