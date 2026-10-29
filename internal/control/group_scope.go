package control

// groupMemberDenied reports whether cmd comes from a group by someone other
// than the owner's own user id (D048). Commands that change how the group's
// agent behaves (/provider, /model) are for the owner there; in private chats
// the existing rules apply unchanged.
func groupMemberDenied(c Caller) bool {
	return c.ChatID < 0 && !c.IsGroupOwner && !c.IsOwner
}

func notAuthorizedInGroup() Response {
	return Response{Text: "Not authorized.", Code: 1}
}
