local
function
a
()
return
1
end
do
function
_G.a
()
return
2
end
end
local
result=
_G
.a(
)
_G
.a
=
nil
return
result
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__