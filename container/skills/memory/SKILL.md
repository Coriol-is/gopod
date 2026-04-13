# Memory skill

You have access to a long-term memory system via gopod's memory API.

## Automatic context

Your system prompt already includes the top-5 most relevant memories
from previous conversations. You don't need to search for basic
context — it's already there.

## When you need more

Use the memory API via curl when:
- The user asks you to recall something specific not in your context
- You want to store an important fact, decision, or preference
- You need to browse all stored memories

## API (via Bash tool)

### Search memories
```bash
curl -s "http://host.docker.internal:9876/memory/search?q=YOUR+QUERY&chat=${GOPOD_CHAT_FOLDER}&k=5"
```

### Store a new memory
```bash
curl -s -X POST "http://host.docker.internal:9876/memory/add" \
  -H "Content-Type: application/json" \
  -d '{"chat":"'${GOPOD_CHAT_FOLDER}'","kind":"fact","content":"The fact to remember"}'
```

kind must be one of: `fact`, `decision`, `preference`, `hypothesis`

### List recent memories
```bash
curl -s "http://host.docker.internal:9876/memory/list?chat=${GOPOD_CHAT_FOLDER}&limit=10"
```

## Guidelines

- **Store**: user preferences, decisions, project facts, important context
- **Don't store**: greetings, small talk, ephemeral process discussion
- **Search before answering** if the user says "remember when..." or "what did I say about..."
- Results are JSON arrays — parse them and present naturally
