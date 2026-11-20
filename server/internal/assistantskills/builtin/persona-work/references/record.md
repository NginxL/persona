# To-dos and reminders

Use `todo_create` for a requested item, with a short title in the person's language and details in notes. Set a due date or reminder from their request. A request to remember an action ends with the saved item. Use an external task service only when the person names it.

If they refer to an existing item, find it with `todo_list` and inspect it with `todo_get`. Update that item. Use the existing long-term knowledge-base project when clear, leaving the project unset when unknown. In a direction, record each piece under that direction so the owner can see its progress.

When the owner asks main to keep track of a question and report back, create a concern with the question as its title. Find open concerns with `todo_list`. Once there is an answer, tell the owner what happened and record that same answer with `concern_report`.

For a requested link or binding change, see [Repositories and links](repositories.md).
