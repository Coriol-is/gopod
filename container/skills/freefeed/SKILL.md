---
name: freefeed
description: Read and write to FreeFeed social network via the frf CLI.
  Use when the user asks about FreeFeed, their feed, posts, comments,
  or direct messages.
---

# FreeFeed

You have the `frf` CLI tool available at `/usr/local/bin/frf`.
It connects to FreeFeed using the `FREEFEED_APP_TOKEN` env var
(already set in your environment).

## Reading

```bash
frf timeline                          # home feed
frf timeline discussions              # discussions
frf timeline directs                  # direct messages
frf timeline posts <username>         # user's posts
frf timeline likes <username>         # user's likes
frf post get <id>                     # single post with all comments
frf search "query"                    # search (supports from:, intitle:, incomment:)
```

Pagination: `--limit 10 --page 2`

## Writing

```bash
frf post create "Hello, FreeFeed!"
frf post create "Post" --group group1,group2
frf comment add <post-id> "Nice post!"
frf direct create "Hey" --to user1,user2
```

## Social

```bash
frf user me                           # current user
frf user profile <username>           # user profile
frf user subscribers <username>       # followers
frf user subscriptions <username>     # following
frf group list                        # my groups
frf group timeline <name>             # group feed
```

## Post actions

```bash
frf post like <id>
frf post unlike <id>
frf post update <id> "new text"
frf post delete <id>
```

## Guidelines

- Output is plain text, readable directly — no JSON parsing needed.
- Post IDs are UUIDs shown as `id:<uuid>` in output.
- Before posting or commenting, confirm with the user unless they
  explicitly asked you to post.
- For long feeds, use `--limit` to avoid flooding the conversation.
