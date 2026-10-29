local
a=
function(f)
return
f
end
local
b=
a(
function()
return
3
end
)(
)
return
b
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__