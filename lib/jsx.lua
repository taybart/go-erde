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
-- Components are called right away w/ their props (`props.children` holds the
-- children). `nil`, `false`, and `true` children are dropped, so
-- `{cond && <b/>}` works as expected.
function M.h(tag, props, ...)
  local children, n, args = {}, select('#', ...), { ... }
  for i = 1, n do
    local child = args[i]
    if child ~= nil and child ~= false and child ~= true then
      children[#children + 1] = child
    end
  end

  if tag == nil then
    return children
  elseif type(tag) == 'string' then
    return setmetatable({ tag = tag, props = props or {}, children = children }, Node)
  elseif isCallable(tag) then
    props = props or {}
    props.children = children
    return tag(props)
  end

  error('jsx: invalid element type (' .. type(tag) .. ')', 2)
end

-- raw(html) marks a string as trusted HTML, which is rendered as is.
function M.raw(html)
  return setmetatable({ tostring(html) }, Raw)
end

-- isElement(v) returns whether `v` was built by `h` from a tag name.
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

local renderTo

local function renderElement(buf, node)
  local tag, props = node.tag, node.props
  buf[#buf + 1] = '<' .. tag

  -- Sorted, so output is deterministic.
  for _, key in ipairs(sortedKeys(props)) do
    local value = props[key]
    if key == 'children' or value == nil or value == false or type(value) == 'function' then
      -- skipped (functions are event handlers, which only mean something in
      -- the browser)
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
    if #node.children > 0 then
      error('jsx: <' .. tag .. '> can\'t have children', 0)
    end
    return
  end

  for _, child in ipairs(node.children) do
    renderTo(buf, child, RAW_TEXT[tag])
  end
  buf[#buf + 1] = '</' .. tag .. '>'
end

function renderTo(buf, node, rawText)
  local t = type(node)

  if node == nil or node == false or node == true then
    return
  elseif t == 'string' then
    buf[#buf + 1] = rawText and node or escape(node)
  elseif t == 'number' then
    buf[#buf + 1] = tostring(node)
  elseif t == 'table' then
    local mt = getmetatable(node)
    if mt == Node then
      renderElement(buf, node)
    elseif mt == Raw then
      buf[#buf + 1] = node[1]
    elseif mt ~= nil and mt.__tostring then
      renderTo(buf, tostring(node), rawText)
    else -- list of children (ex. a fragment, or the result of `map`)
      for i = 1, #node do
        renderTo(buf, node[i], rawText)
      end
    end
  elseif t == 'function' then -- lazy child
    renderTo(buf, node(), rawText)
  else
    error('jsx: can\'t render a ' .. t, 0)
  end
end

-- render(node) renders a node (or string, list, etc.) to an HTML string.
function M.render(node)
  local buf = {}
  renderTo(buf, node, false)
  return table.concat(buf)
end

-- document(node) is like render, but prefixed w/ `<!DOCTYPE html>`.
function M.document(node)
  return '<!DOCTYPE html>' .. M.render(node)
end

M.escape = escape

Node.__tostring = M.render
Raw.__tostring = function(self) return self[1] end

return M
