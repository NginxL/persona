CREATE TABLE todo_images (
    todo_id  text NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    id       text NOT NULL,
    position integer NOT NULL,
    mime     text NOT NULL,
    width    integer NOT NULL,
    height   integer NOT NULL,
    name     text NOT NULL DEFAULT '',
    data     bytea NOT NULL,
    PRIMARY KEY (todo_id, id),
    UNIQUE (todo_id, position)
);
