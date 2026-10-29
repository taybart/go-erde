local
a=
{
b=
3
,
}
a['end']
=
function
(self)
return
self
.b
end
return
a
['end'](
a
)
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__