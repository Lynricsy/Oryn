-- M33: generative UI master switch (whether agents may render views with render_ui / render_html).

-- name: GetOwnerGenerativeUI :one
SELECT generative_ui FROM owner WHERE id = 1;

-- name: SetOwnerGenerativeUI :execrows
UPDATE owner SET generative_ui = @generative_ui WHERE id = 1;
