local
a=
{
b=
1
,
}
function
a:c
()
return
self
.b
end
return
a
:c(
)
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__