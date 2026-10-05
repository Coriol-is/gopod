# Memory skill

You have access to a long-term memory system. Use it to:
- **Search** past facts, decisions, and preferences
- **Store** new important facts the user tells you
- **List** recent memories

Every call needs the capability from the environment:
`-H "Authorization: Bearer $GOPOD_MEMORY_CAPABILITY"`. It is valid for this
turn only and already identifies your chat; never pass a chat name.

## Commands (via curl in your shell tool)

### Search memory
```bash
curl -s -H "Authorization: Bearer $GOPOD_MEMORY_CAPABILITY" \
  "http://host.docker.internal:9876/memory/search?q=YOUR+QUERY&k=5"
```

### Remember a fact
```bash
curl -s -X POST -H "Authorization: Bearer $GOPOD_MEMORY_CAPABILITY" \
  -H "Content-Type: application/json" \
  -d '{"kind":"fact","content":"THE FACT TO REMEMBER"}' \
  "http://host.docker.internal:9876/memory/add"
```

### List recent memories
```bash
curl -s -H "Authorization: Bearer $GOPOD_MEMORY_CAPABILITY" \
  "http://host.docker.internal:9876/memory/list?limit=10"
```

## When to use
- **Search**: when the user asks you to recall something, or when you need context from past conversations
- **Store**: when the user states a preference, makes a decision, or tells you an important fact
- **Don't store**: greetings, ephemeral questions, process discussion

## Important
- The system prompt already includes your top-5 most relevant memories automatically
- Use explicit search only when you need MORE context than what's already provided
- kind must be one of: fact, decision, preference, hypothesis
