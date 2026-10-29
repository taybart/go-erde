local
a=
{
b=
-
1
,
}
local
c=
{
['end']=
function()
return
a
end
,
}
local __ERDE_TMP_4__ =
c
['end'](
c
)
__ERDE_TMP_4__
.b
=
__ERDE_TMP_4__
.b
+
(
5
)
return
a
.b
-- Compiled with Erde 1.0.0-1 w/ Lua target jit
-- __ERDE_COMPILED__