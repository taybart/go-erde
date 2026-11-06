-- erde.jsx: runtime for go-erde's `jsx` experiment (`erde -X jsx`).
--
-- Elements are lowered to calls of `h(tag, props, ...children)`:
--
--   local { h, render } = require('erde.jsx')
--   print(render(<p class="greeting">hello {name}!</p>))
--
-- `h` builds a tree of nodes, which `render` turns into an HTML string. Text
-- and attribute values are escaped; use `raw` for trusted HTML.
--
-- Plain Lua (5.1+), so it also works w/ the output of `erde compile`.

local M = {}

local Node = {} -- metatable of elements
local Raw = {} -- metatable of trusted HTML

local VOID = {
  area = true, base = true, br = true, col = true, embed = true, hr = true,
  img = true, input = true, link = true, meta = true, param = true,
  source = true, track = true, wbr = true,
}

-- Elements whose text content is not HTML (so it isn't escaped).
local RAW_TEXT = { script = true, style = true }

local ESCAPES = {
  ['&'] = '&amp;', ['<'] = '&lt;', ['>'] = '&gt;', ['"'] = '&quot;', ["'"] = '&#39;',
}

local function escape(s)
  return (tostring(s):gsub('[&<>"\']', ESCAPES))
end

local function isCallable(v)
  if type(v) == 'function' then return true end
  local mt = getmetatable(v)
  return type(mt) == 'table' and mt.__call ~= nil
end

-- h(tag, props, ...children)
--   tag: string (element), function (component), or nil (fragment)
--
-- Components aren't called here, but when they're rendered (top down, see
-- renderComponent), w/ their props (`props.children` holds the children).
-- `nil`, `false`, and `true` children render nothing, so `{cond && <b/>}`
-- works as expected. They're kept (nil as false) as placeholders, so a
-- condition doesn't change the position (and so the state) of the children
-- after it.
function M.h(tag, props, ...)
  local children, n, args = {}, select('#', ...), { ... }
  for i = 1, n do
    local child = args[i]
    if child == nil then
      child = false
    end
    children[i] = child
  end

  if tag == nil then
    return children
  elseif type(tag) == 'string' then
    return setmetatable({ tag = tag, props = props or {}, children = children }, Node)
  elseif isCallable(tag) then
    props = props or {}
    props.children = children
    return setmetatable({ component = tag, props = props }, Node)
  end

  error('jsx: invalid element type (' .. type(tag) .. ')', 2)
end

-- raw(html) marks a string as trusted HTML, which is rendered as is.
function M.raw(html)
  return setmetatable({ tostring(html) }, Raw)
end

-- isElement(v) returns whether `v` was built by `h` (an element or a
-- component).
function M.isElement(v)
  return getmetatable(v) == Node
end

-- map(list, fn) returns a list of `fn(value, index)` (for rendering lists:
-- `{map(items, item -> <li>{item}</li>)}`).
function M.map(list, fn)
  local out = {}
  for i, value in ipairs(list) do
    out[#out + 1] = fn(value, i)
  end
  return out
end

local function sortedKeys(t)
  local keys = {}
  for key in pairs(t) do
    if type(key) == 'string' then keys[#keys + 1] = key end
  end
  table.sort(keys)
  return keys
end

-- `style={{ color = 'red', ['font-size'] = '2em' }}`
local function styleValue(style)
  local parts = {}
  for _, key in ipairs(sortedKeys(style)) do
    parts[#parts + 1] = key .. ': ' .. tostring(style[key])
  end
  return table.concat(parts, '; ')
end

-- `class={{ 'btn', 'btn-lg', active = isActive }}`
local function classValue(class)
  local parts = {}
  for _, name in ipairs(class) do
    if name then parts[#parts + 1] = tostring(name) end
  end
  for _, key in ipairs(sortedKeys(class)) do
    if class[key] then parts[#parts + 1] = key end
  end
  return table.concat(parts, ' ')
end

-- Actions (see `mount`): functions in `on*` attributes, by id. Ids are the
-- id of the component they're in and their position in it (ex. `r:1.2:3#1`),
-- so they don't change when other components do.
local actions = {}
local mounted -- { root = fn, rpc = url, state = id, morph = url }

-- `onclick`, `onClick`, and `on:click` are all `click`.
local function eventName(key)
  local event = key:match('^on:(.+)$') or key:match('^on(%a.*)$')
  return event and event:lower()
end

-- js string literal (single quoted), safe in html attributes and <script>
local function jsString(s)
  return "'" .. tostring(s):gsub('[%c\'"\\<>&]', function(c)
    return string.format('\\u%04x', c:byte())
  end) .. "'"
end

-- Component instances. Each component rendered gets an id from its position
-- in the tree (its parent's id, its index among its siblings or its `key`,
-- and which component it is), which is what its state is kept by (see
-- `state`). Instances that aren't rendered again are dropped after each
-- render.
local instances = {} -- id -> { hooks = {} }
local seen = {} -- ids rendered in the current render
local current -- { instance, hook } of the component being rendered

-- What each component rendered in the current render (for `dispatch` to send
-- only what changed): id -> {
--   html,   -- its html
--   shell,  -- its html w/ child components replaced by `<!--id-->`, so it
--           -- only changes if the component's own markup does
--   parent, -- id of the parent component ('r' for the root)
--   tag,    -- tag of its root element, if it has one (w/ its id in
--           -- `data-erde`, so the client can find it)
-- }
local rendered = {}
-- `rendered` of the last (outermost) render, and the root's shell
local lastRender

-- The component being rendered (or the root): { id, children, nextAction },
-- where children are the indices of child components' html in its buffer.
local frame

local componentIds = setmetatable({}, { __mode = 'k' })
local nextComponentId = 0
local function componentId(fn)
  local id = componentIds[fn]
  if not id then
    nextComponentId = nextComponentId + 1
    id = nextComponentId
    componentIds[fn] = id
  end
  return id
end

-- slot is the path of the `i`th child of `path`.
local function slot(path, i, child)
  if getmetatable(child) == Node and child.props.key ~= nil then
    -- no spaces, ids are space separated in `data-erde`
    return path .. '.k' .. tostring(child.props.key):gsub('%s', '_')
  end
  return path .. '.' .. i
end

local renderTo

-- `owners` are the ids of the components whose root is this element (in
-- `data-erde` on mounted pages).
local function renderElement(buf, node, path, owners)
  local tag, props = node.tag, node.props
  buf[#buf + 1] = '<' .. tag
  if owners and mounted then
    buf[#buf + 1] = ' data-erde="' .. escape(table.concat(owners, ' ')) .. '"'
    for _, owner in ipairs(owners) do
      if rendered[owner] then
        rendered[owner].tag = tag
      end
    end
  end

  -- Sorted, so output is deterministic.
  for _, key in ipairs(sortedKeys(props)) do
    local value = props[key]
    local event = type(value) == 'function' and mounted and eventName(key)
    if event then
      frame.nextAction = frame.nextAction + 1
      local id = frame.id .. '#' .. frame.nextAction
      actions[id] = value
      buf[#buf + 1] = ' on' .. event .. '="erde.rpc(' .. jsString(id) .. ', event)"'
    elseif key == 'children' or key == 'key' or value == nil or value == false or type(value) == 'function' then
      -- skipped (functions are event handlers, which only mean something in
      -- the browser, see `mount`)
    elseif value == true then
      buf[#buf + 1] = ' ' .. key
    else
      if type(value) == 'table' and getmetatable(value) == nil then
        value = key == 'style' and styleValue(value) or classValue(value)
      end
      buf[#buf + 1] = ' ' .. key .. '="' .. escape(value) .. '"'
    end
  end

  buf[#buf + 1] = '>'

  if VOID[tag] then
    for _, child in ipairs(node.children) do
      if child ~= false and child ~= true then
        error('jsx: <' .. tag .. '> can\'t have children', 0)
      end
    end
    return
  end

  for i, child in ipairs(node.children) do
    renderTo(buf, child, RAW_TEXT[tag], slot(path, i, child))
  end
  buf[#buf + 1] = '</' .. tag .. '>'
end

-- shell is `buf` w/ the html of child components (at `children` indices)
-- replaced by markers.
local function shell(buf, children)
  for i, id in pairs(children) do
    buf[i] = '<!--' .. id .. '-->'
  end
  return table.concat(buf)
end

local function renderComponent(buf, node, rawText, path, owners)
  local id = path .. ':' .. componentId(node.component)
  seen[id] = true
  local instance = instances[id]
  if not instance then
    instance = { hooks = {} }
    instances[id] = instance
  end
  local entry = { parent = frame.id }
  rendered[id] = entry

  local parent, parentFrame = current, frame
  current = { instance = instance, hook = 0 }
  frame = { id = id, children = {}, nextAction = 0 }
  local ok, result = pcall(node.component, node.props)
  current = parent
  if not ok then
    error(result, 0)
  end

  local ids = { id }
  for i, owner in ipairs(owners or {}) do
    ids[i + 1] = owner
  end
  local own = {}
  renderTo(own, result, rawText, id, ids)

  entry.html = table.concat(own)
  entry.shell = shell(own, frame.children)
  frame = parentFrame

  buf[#buf + 1] = entry.html
  frame.children[#buf] = id
end

function renderTo(buf, node, rawText, path, owners)
  local t = type(node)

  if node == nil or node == false or node == true then
    return
  elseif t == 'string' then
    buf[#buf + 1] = rawText and node or escape(node)
  elseif t == 'number' then
    buf[#buf + 1] = tostring(node)
  elseif t == 'table' then
    local mt = getmetatable(node)
    if mt == Node and node.component then
      renderComponent(buf, node, rawText, path, owners)
    elseif mt == Node then
      renderElement(buf, node, path, owners)
    elseif mt == Raw then
      buf[#buf + 1] = node[1]
    elseif mt ~= nil and mt.__tostring then
      renderTo(buf, tostring(node), rawText, path)
    else -- list of children (ex. a fragment, or the result of `map`)
      for i = 1, #node do
        renderTo(buf, node[i], rawText, slot(path, i, node[i]))
      end
    end
  elseif t == 'function' then -- lazy child
    renderTo(buf, node(), rawText, path, owners)
  else
    error('jsx: can\'t render a ' .. t, 0)
  end
end

local renderDepth, nestedRenders = 0, 0

-- render(node) renders a node (or string, list, etc.) to an HTML string.
function M.render(node)
  -- renders can nest (ex. `tostring(<b/>)` in a component), only the
  -- outermost one starts / finishes a render
  local outermost = renderDepth == 0
  local root = 'r'
  local parent, parentFrame = current, frame
  if outermost then
    actions, seen, rendered, nestedRenders = {}, {}, {}, 0
  else
    nestedRenders = nestedRenders + 1
    root = 'n' .. nestedRenders
  end
  frame = { id = root, children = {}, nextAction = 0 }

  renderDepth = renderDepth + 1
  local buf = {}
  local ok, err = pcall(renderTo, buf, node, false, root)
  renderDepth = renderDepth - 1
  local rootFrame = frame
  current, frame = parent, parentFrame
  if not ok then
    error(err, 0)
  end

  local html = table.concat(buf)
  if outermost then
    for id in pairs(instances) do
      if not seen[id] then
        instances[id] = nil
      end
    end
    lastRender = { instances = rendered, shell = shell(buf, rootFrame.children) }
  end
  return html
end

-- state(init) returns the state of the component being rendered: a table
-- that's kept between renders (ex. after actions on a mounted page). `init`
-- (a table, or a function returning one) is only used the first time. A
-- component can have several (they're kept by call order, so don't call
-- `state` conditionally).
--
--   local Counter = () -> {
--     local s = state({ count = 0 })
--     return <button onclick={() -> { s.count += 1 }}>{s.count}</button>
--   }
function M.state(init)
  if not current then
    error('jsx: state must be called while rendering a component', 2)
  end
  current.hook = current.hook + 1
  local hooks = current.instance.hooks
  local value = hooks[current.hook]
  if value == nil then
    if type(init) == 'function' then
      init = init()
    end
    value = init == nil and {} or init
    if type(value) ~= 'table' then
      error('jsx: state must be a table (got ' .. type(value) .. ')', 2)
    end
    hooks[current.hook] = value
  end
  return value
end

-- Client side of actions: calls the rpc endpoint w/ the action id and a
-- summary of the event, then updates the page w/ the response (see
-- `dispatch`): each `<template data-erde-target="id">` replaces the component
-- w/ that id, otherwise it's the whole page. Updates are morphed (w/
-- idiomorph, see MORPH), or swapped if it isn't loaded. Calls are queued, so
-- they run in order. If the server state is gone (ex. after a restart), or the
-- page is out of sync, it reloads.
local CLIENT = [[
(() => {
  const cfg = %s
  let queue = Promise.resolve()
  const morph = (from, to) => {
    if (window.Idiomorph) {
      // keeps focus, selection, and what's being typed in the focused input
      Idiomorph.morph(from, to, { ignoreActiveValue: true })
    } else {
      from.replaceWith(to)
    }
  }
  const summarize = (e) => {
    const t = e.target || {}
    const data = { type: e.type }
    if (typeof t.value === 'string') data.value = t.value
    if (t.type === 'checkbox' || t.type === 'radio') data.checked = t.checked
    if (typeof e.key === 'string') data.key = e.key
    if (e.type === 'submit') {
      e.preventDefault()
      data.form = Object.fromEntries(new FormData(t))
    }
    return data
  }
  window.erde = {
    rpc(id, e) {
      const data = summarize(e)
      queue = queue.then(async () => {
        const res = await fetch(cfg.rpc, {
          method: 'POST',
          headers: { ...cfg.headers, 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, data }),
        })
        if (res.status === 410) return location.reload()
        if (!res.ok) throw new Error(`erde.rpc ${id}: ${res.status} ${await res.text()}`)
        const html = await res.text()
        const page = new DOMParser().parseFromString(html, 'text/html')
        const fragments = page.querySelectorAll('template[data-erde-target]')
        if (fragments.length === 0) {
          if (html.trim() !== '') morph(document.body, page.body)
          return
        }
        for (const fragment of fragments) {
          const id = CSS.escape(fragment.dataset.erdeTarget)
          const target = document.querySelector(`[data-erde~="${id}"]`)
          const next = fragment.content.firstElementChild
          if (!target || !next) return location.reload()
          morph(target, next)
        }
      }).catch(console.error)
    },
  }
})()
]]

-- https://github.com/bigskysoftware/idiomorph
local MORPH = 'https://unpkg.com/idiomorph@0.8.0/dist/idiomorph.min.js'


-- mount(root, { rpc, headers, morph }) renders `root()` as the page, and
-- makes functions in `on*` attributes work: they're registered as actions,
-- and clicking (etc.) POSTs `{ id, data }` (json) to the `rpc` url, which
-- should respond w/ `dispatch(id, data)` (what changed on the page).
-- `headers` are sent along (ex. to find this page's state on the server). A
-- 410 (gone) response reloads the page. `morph` is the url of idiomorph
-- (default from unpkg), or false to swap elements instead.
--
-- The host has to keep this lua state alive between the page and its actions
-- (ex. a rest `page` handler, which does all of this for you).
function M.mount(root, opts)
  opts = opts or {}
  assert(type(root) == 'function', 'jsx: mount requires a function')
  assert(opts.rpc, 'jsx: mount requires an rpc url (ex. { rpc = "/actions" })')
  local morph = opts.morph
  if morph == nil then morph = MORPH end
  mounted = { root = root, rpc = opts.rpc, headers = opts.headers or {}, morph = morph }
  return root()
end

-- Root elements that can't be replaced on their own (or be in a <template>).
local DOCUMENT_TAGS = { html = true, head = true, body = true }

-- changed returns the components that changed between two renders, as
-- `<template data-erde-target="id">html</template>`s, or nil if the whole
-- page has to be sent.
local function changed(before, after)
  if before.shell ~= after.shell then
    return nil -- something outside of any component
  end

  local targets = {}
  for id, entry in pairs(after.instances) do
    local previous = before.instances[id]
    -- (new / removed components change their parent's shell)
    if id:sub(1, 1) == 'r' and previous and previous.shell ~= entry.shell then
      -- w/o a root element of its own, it's replaced w/ its parent
      local target = id
      while target ~= 'r' and not after.instances[target].tag do
        target = after.instances[target].parent
      end
      if target == 'r' or DOCUMENT_TAGS[after.instances[target].tag] then
        return nil
      end
      targets[target] = true
    end
  end

  -- components replaced w/ a parent don't need to be sent on their own
  local ids = {}
  for id in pairs(targets) do
    local parent, covered = after.instances[id].parent, false
    while parent ~= 'r' and not covered do
      covered = targets[parent] or false
      parent = after.instances[parent].parent
    end
    if not covered then
      ids[#ids + 1] = id
    end
  end
  table.sort(ids)

  local out = {}
  for _, id in ipairs(ids) do
    out[#out + 1] = '<template data-erde-target="' .. escape(id) .. '">' .. after.instances[id].html .. '</template>'
  end
  return table.concat(out)
end

-- dispatch(id, data) runs an action w/ the event `data` sent by the client,
-- and returns what changed on the page as a response table ({ headers, body },
-- ex. for a rest handler): the components that changed (their html, by id),
-- or the whole page when something outside of a component (with its own root
-- element) changed. Nothing changed is an empty body.
function M.dispatch(id, data)
  assert(mounted, 'jsx: dispatch requires a mounted page (see mount)')
  local action = actions[id]
  if not action then
    error('jsx: unknown action ' .. tostring(id), 0)
  end

  local before = lastRender
  action(data or {})
  local page = M.document(mounted.root())

  local body = before and changed(before, lastRender) or page
  return { headers = { ['Content-Type'] = 'text/html; charset=utf-8' }, body = body }
end

-- isInteractive returns whether the last render of a mounted page had any
-- actions (if not, the page is static: it never calls the server back, so
-- there's nothing to keep around for it).
function M.isInteractive()
  return mounted ~= nil and next(actions) ~= nil
end

-- document(node) is like render, but prefixed w/ `<!DOCTYPE html>`. For a
-- mounted page w/ actions, it includes the client script.
function M.document(node)
  local html = '<!DOCTYPE html>' .. M.render(node)
  if not M.isInteractive() then
    return html
  end

  local headers = {}
  for _, key in ipairs(sortedKeys(mounted.headers)) do
    headers[#headers + 1] = jsString(key) .. ': ' .. jsString(mounted.headers[key])
  end
  local cfg = '{ rpc: ' .. jsString(mounted.rpc) .. ', headers: { ' .. table.concat(headers, ', ') .. ' } }'
  local script = '<script>' .. CLIENT:format(cfg) .. '</script>'
  if mounted.morph then
    script = '<script src="' .. escape(mounted.morph) .. '"></script>' .. script
  end
  local bodyEnd = html:find('</body>[^<]*$') or html:find('</html>[^<]*$')
  if bodyEnd then
    return html:sub(1, bodyEnd - 1) .. script .. html:sub(bodyEnd)
  end
  return html .. script
end

M.escape = escape

Node.__tostring = M.render
Raw.__tostring = function(self) return self[1] end

return M
