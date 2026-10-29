local
a=
function(...)
local b = { ... }
return
b
end
return
a(
5
,
6
)
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__