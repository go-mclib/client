package memory

type Memory struct {
	ChatChainStore *ChatChainStore
}

func NewMemory() *Memory {
	return &Memory{
		ChatChainStore: NewChatChainStore(),
	}
}
